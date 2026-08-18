// Command terva-telegram-connector is the out-of-process flavor of
// terva's built-in telegram connector. The in-tree connector remains
// the default-on reference implementation; this binary exists to
// dogfood the external connector path (connproto over stdio, spawned
// by the proxy in packages/agent/chat/external) against a real
// service, and to serve as the canonical connsdk example.
//
// It registers as "telegram-ext" so it never collides with the
// compiled-in "telegram" service, and keeps its own token in
// $TERVA_HOME/connectors/telegram-ext/config.json — setup here never
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
	"encoding/json"
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
	"terva.sh/terva/packages/secretstore"
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
			cfg, err := loadConfig()
			return err == nil && cfg.BotToken != ""
		},
		// Declaring the SAME SealedState we load and save with is what lets
		// terva re-seal this file during a key rotation without ever holding
		// this connector's key. Enforced by TestAConnectorThatSealsAlsoDeclares.
		Secrets: &state,
	})
}

// state is this connector's own sealed store: config.json in its state dir,
// with the bot token sealed to THIS connector's key and terva's, and every
// other field — bot id, pairing claim, poll offset — left plaintext.
//
// It used to reuse telegram.LoadConfig/SaveConfig against the connector's
// directory, on the theory that "passing the connector's state dir where the
// in-tree code passes $TERVA_HOME reuses the package unchanged". That stopped
// being true when the bot tokens moved into the secret store: those functions
// now build the store with config.SecretsCodec(), which resolves terva's
// PRIVATE key from the ambient credential home no matter which directory is
// passed. The dir governed the file location and never the key, so this
// standalone binary was a full holder of the host's at-rest key — the exact
// posture SealedState's doc calls out as the thing it exists to prevent, and
// docs/proposals/secrets-at-rest.md rejects as "the blast radius of a
// compromised connector becomes the whole home rather than its own file".
//
// The other half of that bug: nothing recorded this connector in the component
// registry, and the registry's file path is hardcoded to
// connectors/<name>/config.json — so even the old layout could not have been
// re-sealed. `terva secret rotate --revoke` would leave the token sealed to a
// retired key and the connector could never open its own credential again.
var state = connsdk.SealedState{Name: name, Paths: []string{"/bot_token"}}

// stateDir is the connector's own directory, holding config.json and the
// connector's own key.
func stateDir() string { return state.Dir() }

func newTransport(s connsdk.Session) (connsdk.Transport, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.BotToken == "" {
		return nil, i18n.Errorf("no bot token configured — run `terva bot setup --connector %s`", name)
	}
	conn := telegram.NewConnector(telegram.NewClient(cfg.BotToken), cfg, saveConfig)
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

// loadConfig reads the connector's own config.json, opening the bot token with
// the connector's own key.
//
// A home that still has the pre-SealedState layout is migrated on the way
// through; see migrateFromHostStore for why a failure there is a warning rather
// than an error.
func loadConfig() (telegram.Config, error) {
	if err := migrateFromHostStore(); err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("%s: could not carry the old token forward: %s — run `terva bot setup --connector %s`", name, err, name))
	}
	doc, err := state.Load()
	if err != nil {
		return telegram.Config{}, err
	}
	return configFromDoc(doc)
}

// saveConfig seals the bot token and writes the connector's config.json. It is
// also the persist callback handed to telegram.NewConnector, so a poll-offset
// or pairing update takes the same path as setup.
func saveConfig(c telegram.Config) error {
	doc, err := configDoc(c)
	if err != nil {
		return err
	}
	return state.Save(doc)
}

// configDoc renders a telegram.Config as this connector's file: the struct's
// own JSON, with the bot token restored at the declared pointer.
//
// The token needs restoring because telegram.Config hides it (`json:"-"`) — the
// IN-TREE connector keeps it in terva's secret store instead of in bot.json.
// This goes through a generic map rather than a parallel struct on purpose: a
// field added to telegram.Config round-trips here with no second declaration to
// forget, which is how the two copies drifted apart the first time.
func configDoc(c telegram.Config) ([]byte, error) {
	c.LegacyToken = "" // the in-tree carrier; this file has its own, sealed
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, tokenField)
	if c.BotToken != "" {
		tok, err := json.Marshal(c.BotToken)
		if err != nil {
			return nil, err
		}
		m[tokenField] = tok
	}
	return json.Marshal(m)
}

// configFromDoc is configDoc's inverse.
func configFromDoc(doc []byte) (telegram.Config, error) {
	var c telegram.Config
	if err := json.Unmarshal(doc, &c); err != nil {
		return c, err
	}
	var tok struct {
		Token string `json:"bot_token"`
	}
	if err := json.Unmarshal(doc, &tok); err != nil {
		return c, err
	}
	c.BotToken = tok.Token
	c.LegacyToken = ""
	return c, nil
}

// tokenField is the object key the bot token occupies in config.json. It is
// derived from the declared JSON Pointer rather than written twice: the pointer
// is what SealedState seals and what terva records in the component registry, so
// a key that disagreed with it would write a token the seal never covers.
var tokenField = strings.TrimPrefix(state.Paths[0], "/")

// migrateFromHostStore carries a pre-SealedState install forward, once.
//
// The old layout kept bot.json beside a secrets.json sealed to TERVA's key, so
// reading it one last time through the in-tree loader is the only way to
// recover the token — and it is exactly the dependency this change exists to
// remove, which is why it runs only while the old file is present and the new
// one is not.
//
// Every failure here is reported and swallowed. If the host key has already
// been rotated the old token is unrecoverable no matter what this does, and
// refusing to start would turn "re-run setup" into "the connector is bricked".
func migrateFromHostStore() error {
	legacy := telegram.ConfigPath(stateDir())
	if _, err := os.Stat(legacy); err != nil {
		return nil // nothing to carry forward
	}
	if _, err := os.Stat(state.Path()); err == nil {
		// Already migrated and the old file survived a failed cleanup; drop it
		// rather than reading a stale token over the live one.
		return removeLegacyState(legacy)
	}
	cfg, err := telegram.LoadConfig(stateDir())
	if err != nil {
		return err
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	return removeLegacyState(legacy)
}

// removeLegacyState deletes the old pair. The connector-local secrets.json held
// nothing but this token, sealed to the host key, so leaving it behind would
// leave a credential readable by a key this connector no longer needs.
func removeLegacyState(legacy string) error {
	for _, p := range []string{legacy, filepath.Join(stateDir(), secretstore.FileName)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// setup mirrors the in-tree telegram setup flow against this
// connector's own state dir.
func setup() error {
	cfg, err := loadConfig()
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
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Print("\n" + i18n.T("saved: @%s (id=%d) to %s", me.Username, me.ID, state.Path()) + "\n")
	fmt.Println(i18n.T("next: run `terva bot run --connector %s`, then send /start to your bot.", name))
	return nil
}

func status() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	if cfg.BotToken == "" {
		return i18n.T("not configured (run `terva bot setup --connector %s`)", name), nil
	}
	var b strings.Builder
	b.WriteString(i18n.T("telegram bot: @%s (id=%d)", cfg.BotUsername, cfg.BotID) + "\n")
	b.WriteString(i18n.T("token: %s", maskToken(cfg.BotToken)) + "\n")
	b.WriteString(i18n.T("config file: %s", state.Path()))
	return b.String(), nil
}

func reset() error {
	p := state.Path()
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
