package discord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/connsdk"
)

// maxAttachmentBytes bounds a single inbound attachment download; a
// larger file is skipped with a note in the connector log rather than
// buffered into the host.
const maxAttachmentBytes = 8 << 20

// Transport is the Discord connsdk.Transport: one implementation for
// all three packagings (compiled-in via chat/connlocal, standalone via
// cmd/terva-discord-connector, extension later).
type Transport struct {
	token   string
	dataDir string

	api   api
	botID string

	mu         sync.Mutex
	asks       map[string]*pendingAsk
	membership func(connsdk.Membership) // ReceiveMembership's deliver, once registered
	events     *connsdk.ChatEventSink   // ReceiveChatEvents' sink, once registered
}

// pendingAsk is one live ask message: where it is, what it said, who
// may answer, and where answers go (the connsdk deliver hook).
type pendingAsk struct {
	chatID    string
	messageID string
	text      string
	restrict  []string
	deliver   func(connsdk.Answer)
}

// NewTransport builds the transport. dataDir is the host-assigned
// scratch dir from the session (inbound attachment files land there).
func NewTransport(token, dataDir string) (*Transport, error) {
	if token == "" {
		return nil, fmt.Errorf("no bot token configured — run `terva bot setup --connector discord` first")
	}
	return &Transport{token: token, dataDir: dataDir, asks: map[string]*pendingAsk{}}, nil
}

func (t *Transport) Connect(ctx context.Context) (connsdk.Identity, error) {
	a, err := newAPI(t.token)
	if err != nil {
		return connsdk.Identity{}, err
	}
	id, username, err := a.me(ctx)
	if err != nil {
		return connsdk.Identity{}, fmt.Errorf("token rejected by discord: %w", err)
	}
	t.api = a
	t.botID = id
	return connsdk.Identity{ID: id, Username: username}, nil
}

// Receive opens the gateway and streams normalized messages until ctx
// ends. Gateway blips are the SDK's job to ride through; an open error
// is fatal (the standalone contract). Button interactions ride the
// same gateway session and route to the pending-ask registry.
func (t *Transport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	if err := t.api.open(ctx, func(im inboundMessage) {
		if m, ok := t.normalize(im); ok {
			deliver(m)
		}
	}, func(ii inboundInteraction) {
		t.onInteraction(ctx, ii)
	}, func(mb inboundMembership) {
		t.onMembership(mb)
	}, func(ev inboundChatEvent) {
		t.onChatEvent(ev)
	}); err != nil {
		return fmt.Errorf("open gateway: %w", err)
	}
	defer t.api.close()
	<-ctx.Done()
	return nil
}

// ReceiveMembership implements connsdk.MembershipSource: guild joins
// and leaves surface as admission events on the guild's SYSTEM
// channel (the closest thing a Discord server has to "the chat the
// bot landed in" — approving it admits that channel; other channels
// still need their own /approve). Discord never says who added the
// bot, so By* stay empty.
func (t *Transport) ReceiveMembership(ctx context.Context, deliver func(connsdk.Membership)) error {
	t.mu.Lock()
	t.membership = deliver
	t.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (t *Transport) onMembership(mb inboundMembership) {
	if mb.ChannelID == "" {
		return // no system channel — nothing admittable to point at
	}
	t.mu.Lock()
	deliver := t.membership
	t.mu.Unlock()
	if deliver == nil {
		return
	}
	change := "removed"
	if mb.Added {
		change = "added"
	}
	deliver(connsdk.Membership{
		ChatID: mb.ChannelID, ChatKind: "group", ChatTitle: mb.Title, Change: change,
	})
}

// normalize maps one gateway message onto the wire, applying the
// posture rules:
//   - echo hygiene: never deliver bot/webhook messages (including our
//     own — the cardinal rule from docs/connectors.md);
//   - guild messages without content (the MESSAGE_CONTENT privilege
//     gate strips everything but @mentions of us) are dropped, which
//     is precisely the mention-gated group default;
//   - image attachments are downloaded into the data dir and passed by
//     path, the v1 attachment flow.
func (t *Transport) normalize(im inboundMessage) (connsdk.Message, bool) {
	if im.AuthorIsBot || im.AuthorID == t.botID {
		return connsdk.Message{}, false
	}
	text := strings.TrimSpace(im.Content)
	atts := t.ingest(im)
	if text == "" && len(atts) == 0 {
		return connsdk.Message{}, false
	}
	kind := "dm"
	if im.GuildID != "" {
		kind = "group"
	}
	if im.IsThread {
		kind = "thread"
	}
	return connsdk.Message{
		ID:          im.MessageID,
		TS:          im.TS,
		ChatID:      im.ChannelID,
		ChatKind:    kind,
		UserID:      im.AuthorID,
		Username:    im.AuthorName,
		ReplyTo:     im.ReplyToID,
		Text:        text,
		Entities:    t.mentionEntities(im, text),
		Attachments: atts,
	}, true
}

// mentionEntities emits a bot_mention entity when the message
// mentions us — located at the raw <@id> / <@!id> token when it
// appears in the text (offsets in code points), unlocated (0/0)
// otherwise (e.g. a reply-mention with no textual token). This is the
// signal the host's group mention-gating runs on, and it works
// WITHOUT the privileged content intent: mentioning messages are
// exactly the guild messages Discord delivers with content.
func (t *Transport) mentionEntities(im inboundMessage, text string) []connsdk.Entity {
	return t.mentionEntitiesIn(im.Mentions, text)
}

func (t *Transport) mentionEntitiesIn(mentions []string, text string) []connsdk.Entity {
	mentioned := false
	for _, id := range mentions {
		if id == t.botID {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return nil
	}
	for _, token := range []string{"<@" + t.botID + ">", "<@!" + t.botID + ">"} {
		if i := strings.Index(text, token); i >= 0 {
			return []connsdk.Entity{{
				Kind:   "bot_mention",
				Offset: utf8.RuneCountInString(text[:i]),
				Length: utf8.RuneCountInString(token),
			}}
		}
	}
	return []connsdk.Entity{{Kind: "bot_mention"}}
}

// ingest downloads attachments of every kind to the data dir (stage
// E): images ride the v1 in-memory flow host-side; everything else is
// staged as a labeled file reference. Oversized files are truncated by
// the download cap — the host still sees them, size-labeled.
func (t *Transport) ingest(im inboundMessage) []connsdk.Attachment {
	var out []connsdk.Attachment
	for i, a := range im.Attachments {
		name := fmt.Sprintf("%s-%d-%s", im.MessageID, i, filepath.Base(a.Filename))
		path := filepath.Join(t.dataDir, name)
		if err := download(a.URL, path); err != nil {
			continue // logged by the host as a missing attachment; not fatal
		}
		out = append(out, connsdk.Attachment{
			MimeType: a.ContentType, Path: path,
			Kind: attachmentKind(a.ContentType), Name: a.Filename,
			Size:     a.Size,
			Duration: time.Duration(a.DurationMS) * time.Millisecond,
		})
	}
	return out
}

// attachmentKind maps a mime type onto the wire's kind vocabulary.
// Discord flags voice messages on the MESSAGE, not the attachment, so
// audio stays "audio" here (hosts treat voice and audio alike).
func attachmentKind(contentType string) string {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	default:
		return "document"
	}
}

func download(url, path string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("attachment fetch: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, maxAttachmentBytes))
	return err
}

// ReceiveChatEvents implements connsdk.ChatEventSource (stage D):
// message edits, deletions, and reaction toggles stream to the host.
func (t *Transport) ReceiveChatEvents(ctx context.Context, sink connsdk.ChatEventSink) error {
	t.mu.Lock()
	t.events = &sink
	t.mu.Unlock()
	<-ctx.Done()
	return nil
}

// onChatEvent routes one gateway event onto the sink. Echo hygiene
// applies to reactions exactly as to messages: the bot's own reaction
// toggles (from outbound react commands) never go back to the host.
func (t *Transport) onChatEvent(ev inboundChatEvent) {
	t.mu.Lock()
	sink := t.events
	t.mu.Unlock()
	if sink == nil {
		return
	}
	switch ev.Kind {
	case "edited":
		if sink.Edited == nil || strings.TrimSpace(ev.Content) == "" {
			return // partial update without content — nothing normalizable
		}
		sink.Edited(connsdk.MessageEdited{
			ChatID: ev.ChannelID, ID: ev.MessageID, TS: ev.TS, Text: ev.Content,
			Entities: t.mentionEntitiesIn(ev.Mentions, ev.Content),
		})
	case "deleted":
		if sink.Deleted == nil {
			return
		}
		sink.Deleted(connsdk.MessageDeleted{ChatID: ev.ChannelID, ID: ev.MessageID})
	case "reaction_add", "reaction_remove":
		if sink.Reaction == nil || ev.UserID == t.botID || ev.Emoji == "" {
			return
		}
		sink.Reaction(connsdk.Reaction{
			ChatID: ev.ChannelID, MessageID: ev.MessageID,
			UserID: ev.UserID, Username: ev.Username,
			Key: ev.Emoji, Removed: ev.Kind == "reaction_remove",
		})
	}
}

// EditMessage / React / DeleteMessage implement the stage-D outbound
// surfaces (connsdk.MessageEditor / MessageReactor / MessageDeleter).
func (t *Transport) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	return t.api.editMessage(ctx, chatID, messageID, text)
}

func (t *Transport) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	return t.api.react(ctx, chatID, messageID, key, remove)
}

func (t *Transport) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	return t.api.deleteMessage(ctx, chatID, messageID)
}

func (t *Transport) Send(ctx context.Context, out connsdk.Outgoing) error {
	_, err := t.api.sendMessage(ctx, out.ChatID, out.ReplyTo, out.Text)
	return err
}

// SendWithID is the protocol-2 upgrade: results carry the created
// message's id (connsdk.MessageIDSender).
func (t *Transport) SendWithID(ctx context.Context, out connsdk.Outgoing) (string, error) {
	return t.api.sendMessage(ctx, out.ChatID, out.ReplyTo, out.Text)
}

// SendAsSpeaker renders a message under an alternate display name via
// the channel's managed webhook (connsdk.SpeakerSender — v2 stage H /
// plan stage D3, name-only for now). Webhook messages cannot be real
// replies, so ReplyTo is dropped; where webhooks are impossible (DMs,
// missing MANAGE_WEBHOOKS) the send degrades to the same prefix
// fallback the host would render, because a cast line must never be
// LOST to a permission problem.
func (t *Transport) SendAsSpeaker(ctx context.Context, out connsdk.Outgoing) (string, error) {
	name := sanitizeSpeakerName(speakerName(out.Speaker))
	mid, err := t.api.sendAsWebhook(ctx, out.ChatID, name, out.Text)
	if err == nil {
		return mid, nil
	}
	return t.api.sendMessage(ctx, out.ChatID, "", "**"+name+":** "+out.Text)
}

// StartThread opens a Discord thread (connsdk.Threader — v2 stage I /
// plan stage D4): anchored to a message when the host provides one,
// standalone otherwise. Thread names cap at 100 characters.
func (t *Transport) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	if strings.TrimSpace(name) == "" {
		name = "terva"
	}
	return t.api.createThread(ctx, chatID, fromMessageID, name)
}

func speakerName(sp *connsdk.Speaker) string {
	if sp == nil {
		return ""
	}
	if sp.Name != "" {
		return sp.Name
	}
	return sp.Key
}

// sanitizeSpeakerName applies Discord's webhook-username rules: ≤80
// chars and no "clyde"/"discord" substrings (rejected server-side).
// Offending substrings get a zero-width-joiner wedge so the display
// stays readable.
func sanitizeSpeakerName(name string) string {
	if name == "" {
		return "cast"
	}
	for _, bad := range []string{"clyde", "discord"} {
		for {
			i := strings.Index(strings.ToLower(name), bad)
			if i < 0 {
				break
			}
			name = name[:i+1] + "‍" + name[i+1:]
		}
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	return name
}

func (t *Transport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return t.api.sendFile(ctx, chatID, path, filepath.Base(path), caption)
}

func (t *Transport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return t.api.sendFile(ctx, chatID, path, filepath.Base(path), caption)
}

func (t *Transport) Typing(ctx context.Context, chatID string) error {
	return t.api.typing(ctx, chatID)
}

// Ask renders one interactive question as a Discord message with
// buttons (connsdk.Asker — v2 stage G / plan stage D2). Registration
// happens BEFORE the send: the gateway can deliver the first click
// before the REST create returns.
func (t *Transport) Ask(ctx context.Context, a connsdk.Ask, deliver func(connsdk.Answer)) (string, error) {
	buttons := make([]askButton, 0, len(a.Options))
	for _, o := range a.Options {
		buttons = append(buttons, askButton{Key: o.Key, Label: o.Label, Style: o.Style})
	}
	p := &pendingAsk{chatID: a.ChatID, text: a.Text, restrict: a.RestrictTo, deliver: deliver}
	t.mu.Lock()
	t.asks[a.ID] = p
	t.mu.Unlock()
	mid, err := t.api.sendAsk(ctx, a.ChatID, a.ReplyTo, a.Text, a.ID, buttons)
	if err != nil {
		t.mu.Lock()
		delete(t.asks, a.ID)
		t.mu.Unlock()
		return "", err
	}
	t.mu.Lock()
	p.messageID = mid
	t.mu.Unlock()
	return mid, nil
}

// CloseAsk withdraws the buttons and renders the outcome into the ask
// message (UPDATE_MESSAGE — the audit trail stays in the channel).
// Idempotent: closing an unknown ask is a no-op.
func (t *Transport) CloseAsk(ctx context.Context, askID, outcome string) error {
	t.mu.Lock()
	p := t.asks[askID]
	delete(t.asks, askID)
	var chatID, messageID, text string
	if p != nil {
		chatID, messageID, text = p.chatID, p.messageID, p.text
	}
	t.mu.Unlock()
	if p == nil || messageID == "" {
		return nil
	}
	content := text
	if outcome != "" {
		content += "\n\n▸ " + outcome
	}
	return t.api.updateAskMessage(ctx, chatID, messageID, content)
}

// onInteraction routes one button click: not-ours ignored, stale asks
// get an ephemeral notice, disallowed clickers get the ephemeral
// rejection (no platform hides a button per-user — this is the
// service-side half of the twice-enforced restrict_to), and a valid
// click acks silently and delivers an ATTESTED answer.
func (t *Transport) onInteraction(ctx context.Context, ii inboundInteraction) {
	askID, key, ok := parseAskCustomID(ii.CustomID)
	if !ok {
		return
	}
	t.mu.Lock()
	p := t.asks[askID]
	t.mu.Unlock()
	if p == nil {
		_ = t.api.respondEphemeral(ctx, ii.InteractionID, ii.Token, "this question is no longer active.")
		return
	}
	if len(p.restrict) > 0 && !containsID(p.restrict, ii.UserID) {
		_ = t.api.respondEphemeral(ctx, ii.InteractionID, ii.Token, "this question isn't for you.")
		return
	}
	// Ack inside the 3s deadline; the visible resolution comes with
	// CloseAsk. A failed ack still delivers — the host's answer path
	// must not depend on Discord's callback bookkeeping.
	_ = t.api.ackComponent(ctx, ii.InteractionID, ii.Token)
	p.deliver(connsdk.Answer{
		AskID: askID, Key: key,
		UserID: ii.UserID, Username: ii.Username,
		Attestation: connsdk.AttestationAttested,
	})
}

// parseAskCustomID splits "ask:<askID>:<key>". Ask ids are minted by
// the host frame-id scheme (no colons), so the first colon after the
// prefix ends the id and the key keeps any colons of its own.
func parseAskCustomID(s string) (askID, key string, ok bool) {
	rest, found := strings.CutPrefix(s, "ask:")
	if !found {
		return "", "", false
	}
	i := strings.Index(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func containsID(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Capabilities are the declared limits shared by every packaging of
// this transport.
func Capabilities() connsdk.Capabilities {
	return connsdk.Capabilities{
		// Discord caps bot message content at 2000 chars; the host
		// chunks longer replies.
		MaxTextLen: 2000,
		// Typing indicators expire after ~10s; re-assert at 8.
		TypingRefresh: 8 * time.Second,
		SendsImages:   true,
		SendsFiles:    true,
		// Stage A of the v2 program: real message ids (true reply
		// threading + result.message_id) and chat kinds. Stage G:
		// native asks — buttons, server-attested answers. Stage H:
		// webhook speakers, name-only (per-message avatars need hosted
		// URLs; the upgrade to speaker:full is recorded in the plan).
		// Stage I: work-stream threads. Stage B: bot_mention entities
		// (the mention-gate signal) and guild-level membership events.
		// Stage D: edits, deletes, and reactions in both directions,
		// with Discord's ~1/s edit-stream practice declared. Stage E:
		// labeled attachment kinds.
		Features: []string{"message_ids", "chat_kinds", "asks", "speaker:name_only", "threads_out",
			"entities", "chat_membership",
			"edits_in", "deletes_in", "reactions_in",
			"edits_out", "reactions_out", "deletes_out",
			"attachment_kinds"},
		MinEditInterval: time.Second,
	}
}
