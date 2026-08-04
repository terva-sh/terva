// Command terva-telegram-connector is the out-of-process flavor of
// terva's built-in telegram connector. The in-tree connector remains
// the default-on reference implementation; this binary exists to
// dogfood the external connector path (connproto over stdio, spawned
// by the proxy in packages/agent/chat/external) against a real
// service, and to serve as the canonical connsdk example.
//
// It registers as "telegram-ext" so it never collides with the
// compiled-in "telegram" service, and keeps its own token in
// $TERVA_HOME/connectors/telegram-ext/bot.json — setup here never
// fights the built-in connector's $TERVA_HOME/bot.json.
//
// Install: `go install ./cmd/terva-telegram-connector`, write a
// manifest {"name":"telegram-ext","exec":"terva-telegram-connector"}
// somewhere, then `terva bot link <path>/connector.json` (or pass
// --connector-manifest for a single run). See docs/connectors.md.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/modes/telegram"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

const name = "telegram-ext"

var version = "0.0.0" // -ldflags "-X main.version=..."

func main() {
	// A connector is its own process, so it configures i18n itself from
	// the environment (TERVA_LANG + the operator's $TERVA_HOME/locales
	// overlay) — the host can't inject it across the process boundary.
	_ = i18n.Configure(envcompat.Get("LANG"), envcompat.Home())
	connsdk.Main(connsdk.Config{
		Name:    name,
		Version: version,
		Capabilities: connsdk.Capabilities{
			// Mirrors the in-tree connector: telegram caps messages
			// at 4096 chars, 4000 leaves margin.
			MaxTextLen:    4000,
			TypingRefresh: 4 * time.Second,
			SendsImages:   true,
			SendsFiles:    true,
		},
		NewTransport: newTransport,
		Setup:        setup,
		Status:       status,
		Reset:        reset,
		Configured: func() bool {
			cfg, err := telegram.LoadConfig(stateDir())
			return err == nil && cfg.BotToken != ""
		},
	})
}

// stateDir holds bot.json. telegram.ConfigPath joins "<dir>/bot.json",
// so passing the connector's state dir where the in-tree code passes
// $TERVA_HOME reuses the package unchanged.
func stateDir() string { return connsdk.StateDir(name) }

func newTransport(s connsdk.Session) (connsdk.Transport, error) {
	cfg, err := telegram.LoadConfig(stateDir())
	if err != nil {
		return nil, err
	}
	if cfg.BotToken == "" {
		return nil, i18n.Errorf("no bot token configured — run `terva bot setup --connector %s`", name)
	}
	conn := telegram.NewConnector(telegram.NewClient(cfg.BotToken), cfg, func(next telegram.Config) error {
		return telegram.SaveConfig(stateDir(), next)
	})
	return &transport{conn: conn, dataDir: s.DataDir}, nil
}

// transport adapts the in-tree telegram connector (which speaks chat
// package types and carries images in memory) to the SDK contract
// (attachments by path under the host-assigned data dir).
type transport struct {
	conn    *telegram.Connector
	dataDir string
	seq     atomic.Uint64
}

func (t *transport) Connect(ctx context.Context) (connsdk.Identity, error) {
	id, err := t.conn.Connect(ctx)
	return connsdk.Identity{ID: id.ID, Username: id.Username}, err
}

func (t *transport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	return t.conn.Receive(ctx, func(m chat.Message) {
		// chat.Message carries stage-A identity semantics (the in-tree
		// telegram connector fills ID/TS/ChatKind); pass them through —
		// the SDK downgrades to the v1 shape for older hosts on its own.
		out := connsdk.Message{
			ID: m.ID, TS: m.TS, ChatID: m.ChatID, ChatKind: m.ChatKind, ChatTitle: m.ChatTitle,
			UserID: m.UserID, Username: m.Username,
			ReplyTo: m.ReplyTo, Text: m.Text,
		}
		for _, img := range m.Images {
			path, err := t.writeAttachment(img)
			if err != nil {
				fmt.Fprintln(os.Stderr, i18n.T("attachment dropped: %s", err))
				continue
			}
			out.Attachments = append(out.Attachments, connsdk.Attachment{MimeType: img.MimeType, Path: path})
		}
		deliver(out)
	})
}

// writeAttachment lands one downloaded image in the data dir; terva
// reads and deletes it.
func (t *transport) writeAttachment(img provider.ImageBlock) (string, error) {
	ext := ".jpg"
	switch img.MimeType {
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	// Owner-only: inbound attachments are the user's private chat content.
	if err := privfs.MkdirAll(t.dataDir); err != nil {
		return "", err
	}
	path := filepath.Join(t.dataDir, fmt.Sprintf("in-%d%s", t.seq.Add(1), ext))
	if err := os.WriteFile(path, img.Data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (t *transport) Send(ctx context.Context, out connsdk.Outgoing) error {
	return t.conn.Send(ctx, chat.Outgoing{ChatID: out.ChatID, ReplyTo: out.ReplyTo, Text: out.Text})
}

func (t *transport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return t.conn.SendImage(ctx, chatID, path, caption)
}

func (t *transport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return t.conn.SendFile(ctx, chatID, path, caption)
}

func (t *transport) Typing(ctx context.Context, chatID string) error {
	return t.conn.Typing(ctx, chatID)
}

// setup mirrors the in-tree telegram setup flow against this
// connector's own state dir.
func setup() error {
	cfg, err := telegram.LoadConfig(stateDir())
	if err != nil {
		return err
	}
	fmt.Print(i18n.T("telegram bot token (from @BotFather): "))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return i18n.Errorf("no token provided")
	}
	me, err := telegram.NewClient(token).GetMe(context.Background())
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("token rejected by telegram"), err)
	}
	cfg.BotToken = token
	cfg.BotUsername = me.Username
	cfg.BotID = me.ID
	// Any stored poll offset belongs to whatever bot came before.
	cfg.AllowedUserID = 0
	cfg.LastUpdateID = 0
	if err := telegram.SaveConfig(stateDir(), cfg); err != nil {
		return err
	}
	fmt.Print("\n" + i18n.T("saved: @%s (id=%d) to %s", me.Username, me.ID, telegram.ConfigPath(stateDir())) + "\n")
	fmt.Println(i18n.T("next: run `terva bot run --connector %s`, then send /start to your bot.", name))
	return nil
}

func status() (string, error) {
	cfg, err := telegram.LoadConfig(stateDir())
	if err != nil {
		return "", err
	}
	if cfg.BotToken == "" {
		return i18n.T("not configured (run `terva bot setup --connector %s`)", name), nil
	}
	var b strings.Builder
	b.WriteString(i18n.T("telegram bot: @%s (id=%d)", cfg.BotUsername, cfg.BotID) + "\n")
	b.WriteString(i18n.T("token: %s", maskToken(cfg.BotToken)) + "\n")
	b.WriteString(i18n.T("config file: %s", telegram.ConfigPath(stateDir())))
	return b.String(), nil
}

func reset() error {
	p := telegram.ConfigPath(stateDir())
	err := os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println(i18n.T("no bot config to remove"))
		return nil
	}
	if err == nil {
		fmt.Println(i18n.T("removed %s", p))
	}
	return err
}

// maskToken keeps the bot id prefix and hides the secret body, the
// in-tree connector's convention for paste-safe status output.
func maskToken(tok string) string {
	i := strings.IndexByte(tok, ':')
	if i < 0 || len(tok) < i+9 {
		return "<hidden>"
	}
	body := tok[i+1:]
	return tok[:i+1] + body[:3] + "..." + body[len(body)-3:]
}
