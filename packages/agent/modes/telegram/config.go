// Package telegram implements terva's Telegram bot bridge.
//
// It runs in-process, polling Telegram for DMs and forwarding them to
// a core.Agent. Responses stream back as Telegram messages. Images
// sent by the user are downloaded and attached to the next prompt as
// provider.ImageBlock, so vision-capable models see them the same way
// they would via drag-and-drop in the TUI.
//
// State (bot token, allowed user id) lives in $TERVA_HOME/bot.json.
package telegram

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/secretstore"
)

// Scope is where the bot token lives in the secret store. The rest of this
// file's state — bot id, username, pairing claim, poll offset — stays in
// bot.json, plaintext and inspectable; only the credential moves.
const Scope = "core:bot.telegram"

// tokenKey is the token's key within Scope.
const tokenKey = "token"

// Config is the in-memory state for the telegram bridge. BotToken is NOT
// persisted in bot.json: it round-trips through the secret store, so a home
// with at-rest encryption on has no bot token readable on disk. Every caller
// keeps using the field exactly as before.
type Config struct {
	BotToken      string `json:"-"`
	LegacyToken   string `json:"bot_token,omitempty"` // pre-store homes only; see LoadConfig
	BotUsername   string `json:"bot_username,omitempty"`
	BotID         int64  `json:"bot_id,omitempty"`
	AllowedUserID int64  `json:"allowed_user_id,omitempty"`
	LastUpdateID  int64  `json:"last_update_id,omitempty"`
}

// ConfigPath returns the path to bot.json.
func ConfigPath(tervaHome string) string {
	return filepath.Join(tervaHome, "bot.json")
}

// LoadConfig reads bot.json and fills BotToken from the secret store.
//
// A token still sitting in bot.json (a home that predates the store) is
// honoured and wins nothing: it is used as-is and moves into the store on the
// next SaveConfig, so an existing install keeps working and converts itself
// without a migration step anyone has to run.
func LoadConfig(tervaHome string) (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath(tervaHome))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	v, storeErr := config.SecretStoreIn(tervaHome).Get(Scope, tokenKey)
	switch {
	case storeErr == nil:
		c.BotToken = v.Value
	case errors.Is(storeErr, secretstore.ErrNotFound):
		c.BotToken = c.LegacyToken // may be "" — that is simply "not configured"
	default:
		// A store that exists but cannot be opened must not read as "no bot
		// configured", which would send the user through setup again and
		// overwrite a working token.
		return c, storeErr
	}
	return c, nil
}

// SaveConfig writes the token to the secret store and bot.json atomically.
//
// The store write comes FIRST: if it fails the token is still in bot.json from
// the previous save, which is recoverable. The reverse order would clear the
// legacy copy and then fail to record the new one, losing the credential.
func SaveConfig(tervaHome string, c Config) error {
	if err := privfs.MkdirAll(tervaHome); err != nil {
		return err
	}
	if err := config.SecretStoreIn(tervaHome).Set(Scope, tokenKey, c.BotToken); err != nil {
		return err
	}
	c.LegacyToken = "" // it lives in the store now
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := ConfigPath(tervaHome)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
