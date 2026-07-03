package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/provider"
)

// Connector adapts the Telegram Bot API to chat.Connector: it owns
// long-polling, update normalization (private-chat filtering, caption
// merging, image downloads), and persisting transport state (bot
// identity, poll offset) to bot.json. Policy — pairing, commands,
// queueing, replies — lives in the chat package.
type Connector struct {
	Client *Client
	// Warn receives transport-level log lines (poll errors). Optional.
	Warn func(string)

	mu     sync.Mutex
	config Config
	save   func(Config) error
}

// NewConnector wraps a Telegram client and its persisted config.
// save is called whenever transport state changes (poll offset, bot
// identity, pairing).
func NewConnector(client *Client, cfg Config, save func(Config) error) *Connector {
	return &Connector{Client: client, config: cfg, save: save}
}

func (c *Connector) Name() string { return "telegram" }

func (c *Connector) Capabilities() chat.Capabilities {
	return chat.Capabilities{
		// Telegram caps messages at 4096 chars; 4000 leaves margin.
		MaxTextLen:    4000,
		TypingRefresh: 4 * time.Second,
		SendsImages:   true,
		SendsFiles:    true,
	}
}

func (c *Connector) warn(s string) {
	if c.Warn != nil {
		c.Warn(s)
		return
	}
	fmt.Fprintln(stderr(), s)
}

// Connect validates the token with getMe and syncs the stored bot
// identity.
func (c *Connector) Connect(ctx context.Context) (chat.Identity, error) {
	c.mu.Lock()
	token := c.config.BotToken
	c.mu.Unlock()
	if token == "" {
		return chat.Identity{}, fmt.Errorf("no bot token configured; run `terva telegram-bot setup` first")
	}
	me, err := c.Client.GetMe(ctx)
	if err != nil {
		return chat.Identity{}, fmt.Errorf("getMe: %w", err)
	}
	c.mu.Lock()
	if c.config.BotID != me.ID || c.config.BotUsername != me.Username {
		c.config.BotID = me.ID
		c.config.BotUsername = me.Username
		c.persistLocked()
	}
	c.mu.Unlock()
	return chat.Identity{ID: strconv.FormatInt(me.ID, 10), Username: me.Username}, nil
}

// PairingState returns the persisted claim as a chat.Pairing whose
// Save writes back into bot.json. Telegram user ids are numeric;
// they're carried as strings per the chat contract.
func (c *Connector) PairingState() chat.Pairing {
	c.mu.Lock()
	allowed := c.config.AllowedUserID
	c.mu.Unlock()
	p := chat.Pairing{Save: func(userID string) error {
		id, err := strconv.ParseInt(userID, 10, 64)
		if err != nil {
			return fmt.Errorf("telegram: non-numeric user id %q", userID)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.config.AllowedUserID = id
		return c.persistLocked()
	}}
	if allowed != 0 {
		p.AllowedUserID = strconv.FormatInt(allowed, 10)
	}
	return p
}

// persistLocked saves the config; callers hold c.mu.
func (c *Connector) persistLocked() error {
	if c.save == nil {
		return nil
	}
	return c.save(c.config)
}

// Receive long-polls Telegram and delivers normalized messages until
// ctx ends. Poll errors back off exponentially (1s..30s) and are
// surfaced through Warn; only ctx cancellation ends the loop.
func (c *Connector) Receive(ctx context.Context, handle func(chat.Message)) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		offset := c.config.LastUpdateID + 1
		c.mu.Unlock()
		updates, err := c.Client.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.warn(fmt.Sprintf("telegram: getUpdates error: %v", err))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			if m, ok := c.normalize(ctx, u); ok {
				handle(m)
			}
			c.mu.Lock()
			c.config.LastUpdateID = u.UpdateID
			_ = c.persistLocked()
			c.mu.Unlock()
		}
	}
}

// normalize converts one Telegram update into a chat.Message:
// private DMs from humans only, text + caption merged, photo and
// image-document attachments downloaded.
func (c *Connector) normalize(ctx context.Context, u Update) (chat.Message, bool) {
	msg := u.Message
	if msg == nil {
		msg = u.Edited
	}
	if msg == nil || msg.From == nil || msg.From.IsBot {
		return chat.Message{}, false
	}
	if msg.Chat.Type != "private" {
		return chat.Message{}, false
	}

	text := strings.TrimSpace(msg.Text)
	if msg.Caption != "" {
		if text != "" {
			text += "\n"
		}
		text += msg.Caption
	}

	var images []provider.ImageBlock
	if len(msg.Photo) > 0 {
		// Photos arrive in multiple sizes; take the largest (last).
		largest := msg.Photo[len(msg.Photo)-1]
		if data, mime, err := c.download(ctx, largest.FileID, ""); err == nil {
			images = append(images, provider.ImageBlock{MimeType: mime, Data: data})
		} else {
			c.warn(fmt.Sprintf("telegram: download photo: %v", err))
		}
	}
	if msg.Document != nil && isImageMIME(msg.Document.MimeType) {
		if data, mime, err := c.download(ctx, msg.Document.FileID, msg.Document.MimeType); err == nil {
			images = append(images, provider.ImageBlock{MimeType: mime, Data: data})
		}
	}

	return chat.Message{
		ID:       strconv.Itoa(msg.MessageID),
		TS:       msg.Date * 1000,
		ChatID:   strconv.FormatInt(msg.Chat.ID, 10),
		ChatKind: chatKind(msg.Chat.Type),
		UserID:   strconv.FormatInt(msg.From.ID, 10),
		Username: msg.From.Username,
		Text:     text,
		Images:   images,
	}, true
}

func (c *Connector) Send(ctx context.Context, out chat.Outgoing) error {
	chatID, err := strconv.ParseInt(out.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id %q", out.ChatID)
	}
	replyTo := 0
	if out.ReplyTo != "" {
		replyTo, _ = strconv.Atoi(out.ReplyTo)
	}
	return c.Client.SendMessage(ctx, chatID, out.Text, replyTo)
}

func (c *Connector) SendImage(ctx context.Context, chatID, path, caption string) error {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id %q", chatID)
	}
	return c.Client.SendPhoto(ctx, id, path, caption)
}

func (c *Connector) SendFile(ctx context.Context, chatID, path, caption string) error {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id %q", chatID)
	}
	return c.Client.SendDocument(ctx, id, path, caption)
}

func (c *Connector) Typing(ctx context.Context, chatID string) error {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id %q", chatID)
	}
	return c.Client.SendChatAction(ctx, id, "typing")
}

// download fetches a file from Telegram and returns bytes + mime.
func (c *Connector) download(ctx context.Context, fileID, mime string) ([]byte, string, error) {
	f, err := c.Client.GetFile(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	data, err := c.Client.DownloadFile(ctx, f.FilePath)
	if err != nil {
		return nil, "", err
	}
	if mime == "" {
		mime = guessImageMIME(f.FilePath)
	}
	return data, mime, nil
}

// isImageMIME returns true for MIME types the model can probably
// ingest as a vision input.
func isImageMIME(m string) bool {
	switch strings.ToLower(m) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}

// guessImageMIME infers a mime type from a filename suffix. Falls
// back to image/jpeg because telegram photos are always re-encoded to
// jpeg but getFile's file_path may omit the extension.
func guessImageMIME(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	}
	return "image/jpeg"
}

// chatKind maps telegram chat types onto the wire vocabulary.
func chatKind(t string) string {
	switch t {
	case "private", "":
		return "dm"
	case "group", "supergroup":
		return "group"
	case "channel":
		return "channel"
	}
	return "group"
}
