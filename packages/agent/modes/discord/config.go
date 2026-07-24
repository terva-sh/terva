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

	"terva.sh/terva/packages/privfs"
)

// Config is the on-disk state for the discord connector.
type Config struct {
	BotToken      string `json:"bot_token,omitempty"`
	BotUsername   string `json:"bot_username,omitempty"`
	BotID         string `json:"bot_id,omitempty"`
	AllowedUserID string `json:"allowed_user_id,omitempty"`
}

// ConfigPath returns the path to discord.json.
func ConfigPath(tervaHome string) string {
	return filepath.Join(tervaHome, "discord.json")
}

// LoadConfig reads discord.json, returning a zero Config if it doesn't
// exist.
func LoadConfig(tervaHome string) (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath(tervaHome))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

// SaveConfig writes discord.json (0600 — it holds the bot token).
func SaveConfig(tervaHome string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := privfs.MkdirAll(tervaHome); err != nil {
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
