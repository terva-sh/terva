package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/chat"
)

// init registers telegram in the chat-service registry. Phase 3 of
// the connector plan moves this registration behind a build tag so
// telegram-free builds drop the whole package.
func init() {
	chat.Register(chat.Service{
		Name:         "telegram",
		Configured:   configured,
		NewConnector: newServiceConnector,
		Setup:        setup,
		StatusText:   statusText,
		Reset:        reset,
	})
}

func configured(tervaHome string) bool {
	cfg, err := LoadConfig(tervaHome)
	return err == nil && cfg.BotToken != ""
}

func newServiceConnector(tervaHome string, warn func(string)) (chat.Connector, chat.Pairing, error) {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return nil, chat.Pairing{}, err
	}
	if cfg.BotToken == "" {
		return nil, chat.Pairing{}, fmt.Errorf("no bot token configured — run `terva bot setup` first")
	}
	conn := NewConnector(NewClient(cfg.BotToken), cfg, func(next Config) error {
		return SaveConfig(tervaHome, next)
	})
	conn.Warn = warn
	return conn, conn.PairingState(), nil
}

// setup interactively reads a bot token, verifies it via getMe, and
// saves it (`terva bot setup`).
func setup(tervaHome string) error {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return err
	}

	fmt.Print("telegram bot token (from @BotFather): ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return fmt.Errorf("no token provided")
	}

	client := NewClient(token)
	me, err := client.GetMe(context.Background())
	if err != nil {
		return fmt.Errorf("token rejected by telegram: %w", err)
	}
	cfg.BotToken = token
	cfg.BotUsername = me.Username
	cfg.BotID = me.ID
	// Any stored pairing might be for a different bot; clear it.
	cfg.AllowedUserID = 0
	cfg.LastUpdateID = 0
	if err := SaveConfig(tervaHome, cfg); err != nil {
		return err
	}
	fmt.Printf("\nsaved: @%s (id=%d) to %s\n", me.Username, me.ID, ConfigPath(tervaHome))
	fmt.Println("next: run `terva bot run`, then send /start to your bot from telegram.")
	return nil
}

// statusText renders the config block for `terva bot status` with the
// token masked.
func statusText(tervaHome string) (string, error) {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return "", err
	}
	if cfg.BotToken == "" {
		return "telegram: not configured (run `terva bot setup`)", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "telegram bot: @%s (id=%d)\n", cfg.BotUsername, cfg.BotID)
	fmt.Fprintf(&b, "token:        %s\n", maskToken(cfg.BotToken))
	if cfg.AllowedUserID == 0 {
		b.WriteString("paired with:  (unpaired — send /start from telegram to claim)\n")
	} else {
		fmt.Fprintf(&b, "paired with:  telegram user id %d\n", cfg.AllowedUserID)
	}
	fmt.Fprintf(&b, "last update:  %d\n", cfg.LastUpdateID)
	fmt.Fprintf(&b, "config file:  %s", ConfigPath(tervaHome))
	return b.String(), nil
}

// reset wipes the on-disk bot.json entry.
func reset(tervaHome string) (string, error) {
	p := ConfigPath(tervaHome)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err := os.Remove(p); err != nil {
		return "", err
	}
	return p, nil
}

// maskToken returns "123456:ABC...xyz" so `terva bot status` output can
// be pasted into bug reports without leaking the full token.
func maskToken(tok string) string {
	if len(tok) <= 10 {
		return "<hidden>"
	}
	// telegram tokens look like "123456789:ABCD..." — keep the id,
	// mask the body.
	i := strings.IndexByte(tok, ':')
	if i < 0 {
		return tok[:4] + "..." + tok[len(tok)-4:]
	}
	body := tok[i+1:]
	if len(body) < 8 {
		return tok[:i+1] + "<hidden>"
	}
	return tok[:i+1] + body[:3] + "..." + body[len(body)-3:]
}
