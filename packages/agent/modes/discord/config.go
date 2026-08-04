// Package discord implements terva's built-in Discord connector — the
// connproto-v2 dogfood surface (docs/plans/discord-connector.md).
//
// Unlike telegram's built-in (a native chat.Connector), this package is
// a plain connsdk.Transport: the compiled-in service runs it through
// chat/connlocal's in-process carrier, so every terva run that uses
// Discord speaks the full connector protocol. The same transport wraps
// unchanged into the standalone cmd/terva-discord-connector binary and,
// later, an extension.
//
// All Discord SDK contact lives behind the api seam (api.go) so the
// transport is unit-testable without a network and the SDK is
// swappable. State (bot token, pairing) lives in $TERVA_HOME/discord.json.
package discord

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
// file's state — bot id, username, pairing claim — stays in discord.json,
// plaintext and inspectable; only the credential moves.
const Scope = "core:bot.discord"

// tokenKey is the token's key within Scope.
const tokenKey = "token"

// Config is the in-memory state for the discord connector. BotToken is NOT
// persisted in discord.json: it round-trips through the secret store, so a
// home with at-rest encryption on has no bot token readable on disk. Every
// caller keeps using the field exactly as before.
type Config struct {
	BotToken      string `json:"-"`
	LegacyToken   string `json:"bot_token,omitempty"` // pre-store homes only; see LoadConfig
	BotUsername   string `json:"bot_username,omitempty"`
	BotID         string `json:"bot_id,omitempty"`
	AllowedUserID string `json:"allowed_user_id,omitempty"`
}

// ConfigPath returns the path to discord.json.
func ConfigPath(tervaHome string) string {
	return filepath.Join(tervaHome, "discord.json")
}

// LoadConfig reads discord.json and fills BotToken from the secret store.
//
// A token still sitting in discord.json (a home that predates the store) is
// honoured and moves into the store on the next SaveConfig, so an existing
// install keeps working and converts itself with no migration step to run.
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

// SaveConfig writes the token to the secret store and discord.json.
//
// The store write comes FIRST: if it fails the token is still in discord.json
// from the previous save, which is recoverable. The reverse order would clear
// the legacy copy and then fail to record the new one, losing the credential.
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
	return os.WriteFile(ConfigPath(tervaHome), append(b, '\n'), 0o600)
}

// maskToken keeps just enough of a token to recognize it.
func maskToken(t string) string {
	if len(t) <= 8 {
		return "********"
	}
	return t[:4] + "…" + t[len(t)-4:]
}
