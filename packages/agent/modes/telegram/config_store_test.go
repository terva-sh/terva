package telegram

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
	"terva.sh/terva/packages/testsupport"
)

// encryptingHome pins TERVA_HOME to a fresh dir holding a secrets key, so the
// store seals what it writes.
func encryptingHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, secrets.KeyFileName), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

// The point of the migration: the bot token stops being readable in bot.json,
// while everything else in that file stays inspectable.
func TestBotTokenLeavesBotJSON(t *testing.T) {
	home := encryptingHome(t)

	want := Config{BotToken: "123456:tg-secret-token", BotUsername: "mybot", BotID: 42, AllowedUserID: 7, LastUpdateID: 99}
	if err := SaveConfig(home, want); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tg-secret-token") {
		t.Fatal("the bot token is still readable in bot.json")
	}
	// The rest of the file stays plaintext and inspectable — only the
	// credential moved.
	for _, want := range []string{"mybot", "allowed_user_id", "last_update_id"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("bot.json lost %q; only the token should have moved", want)
		}
	}
	if secrets.IsAgeFile(raw) {
		t.Error("bot.json was encrypted whole; only the token belongs in the store")
	}

	got, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != want.BotToken || got.BotUsername != want.BotUsername || got.LastUpdateID != want.LastUpdateID {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// An existing install must keep working with no migration step anyone has to
// run, and must convert itself on the next save. This is the path that loses
// credentials if it is wrong.
func TestLegacyTokenInBotJSONStillLoadsAndThenMoves(t *testing.T) {
	home := encryptingHome(t)

	// A bot.json written by a terva that predates the store.
	legacy := map[string]any{
		"bot_token": "123456:legacy-token", "bot_username": "oldbot", "allowed_user_id": 7,
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(home), b, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != "123456:legacy-token" {
		t.Fatalf("a pre-store bot.json stopped working: %+v", got)
	}
	if got.BotUsername != "oldbot" {
		t.Errorf("lost the rest of the legacy config: %+v", got)
	}

	// Saving converts it.
	if err := SaveConfig(home, got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-token") {
		t.Fatal("the legacy token survived in bot.json after a save")
	}
	v, err := config.SecretStoreIn(home).Get(Scope, "token")
	if err != nil || v.Value != "123456:legacy-token" {
		t.Fatalf("the token did not move into the store: %+v %v", v, err)
	}
}

// A store that exists but cannot be opened must NOT read as "no bot
// configured" — that would send the user through setup again and overwrite a
// working token with a new one.
func TestUnopenableStoreIsAnErrorNotAnEmptyToken(t *testing.T) {
	home := encryptingHome(t)
	if err := SaveConfig(home, Config{BotToken: "123456:tg-secret", BotUsername: "mybot"}); err != nil {
		t.Fatal(err)
	}
	// The operator loses the key.
	if err := os.Remove(filepath.Join(home, secrets.KeyFileName)); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfig(home)
	if err == nil {
		t.Fatalf("an unreadable store loaded as %+v; setup would run again over a live token", got)
	}
	if got.BotToken != "" {
		t.Error("returned a token alongside the error")
	}
}

// With no key configured everything still works in the clear — the feature is
// inert until someone opts in.
func TestNoKeyStillRoundTrips(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := SaveConfig(home, Config{BotToken: "123456:plain", BotUsername: "b"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != "123456:plain" {
		t.Fatalf("got %+v", got)
	}
	// It is in the store, in the clear, rather than back in bot.json.
	raw, _ := os.ReadFile(ConfigPath(home))
	if strings.Contains(string(raw), "123456:plain") {
		t.Error("the token went back into bot.json when no key was configured")
	}
	if _, err := os.Stat(config.SecretStorePath(home)); err != nil {
		t.Errorf("no store was written: %v", err)
	}
}

// Clearing the token must actually remove it, not leave the old one behind for
// the next load to resurrect.
func TestClearingTheTokenRemovesIt(t *testing.T) {
	home := encryptingHome(t)
	if err := SaveConfig(home, Config{BotToken: "123456:tg-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(home, Config{BotToken: ""}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != "" {
		t.Fatalf("a cleared token came back: %q", got.BotToken)
	}
	if _, err := config.SecretStoreIn(home).Get(Scope, "token"); err == nil {
		t.Error("the value is still in the store")
	} else if !strings.Contains(err.Error(), secretstore.ErrNotFound.Error()) {
		t.Errorf("unexpected error: %v", err)
	}
}
