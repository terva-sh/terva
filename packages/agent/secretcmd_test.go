package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modes/telegram"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

func TestSecretInitEncryptsExistingAuthFile(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-live-1"); err != nil {
		t.Fatal(err)
	}
	if got := fileEncryptionState(config.AuthPath()); got != statePlaintext {
		t.Fatalf("precondition: auth.json should be plaintext, is %s", got)
	}

	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(home, "secrets.key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("init did not write the key file: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key file is not owner-only: %v", fi.Mode())
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets == nil || !strings.HasPrefix(cfg.Secrets.Recipient, "age1") {
		t.Fatalf("recipient not recorded in config: %+v", cfg.Secrets)
	}
	if got := fileEncryptionState(config.AuthPath()); got != stateEncrypted {
		t.Fatalf("auth.json not migrated: %s", got)
	}
	raw, _ := os.ReadFile(config.AuthPath())
	if strings.Contains(string(raw), "sk-live-1") {
		t.Fatal("plaintext credential survives on disk after init")
	}
	// The running process keeps working: the shared store decrypts.
	c, err := config.AuthStoreFor().Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DeepSeek.APIKey != "sk-live-1" {
		t.Fatalf("credential lost across migration: %+v", c.DeepSeek)
	}

	// A second init is a no-op, not an error: a fresh install now configures
	// itself, so this is the path a user following the docs actually takes, and
	// the key must survive it untouched. Replacing a key is `rotate`.
	keyBefore, err := os.ReadFile(filepath.Join(home, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatalf("a second init on a configured home must be a no-op, got %v", err)
	}
	keyAfter, err := os.ReadFile(filepath.Join(home, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(keyBefore) != string(keyAfter) {
		t.Fatal("a second init replaced the key — every file sealed to the old one is now unrecoverable")
	}
	if c, err := config.AuthStoreFor().Load(); err != nil || c.DeepSeek.APIKey != "sk-live-1" {
		t.Fatalf("credential lost across a second init: %+v, %v", c.DeepSeek, err)
	}
}

// A home whose key has gone missing (not restored from backup, a
// --secrets-key-file nobody passed) looks exactly like a never-encrypted home
// to a naive "does a key resolve?" check. Generating a fresh key there would
// strand every byte the old one encrypted — silently and permanently.
func TestSecretInitRefusesWhenTheKeyIsMerelyMissing(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-strandable"); err != nil {
		t.Fatal(err)
	}
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	encrypted, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}

	// The operator loses the key but keeps the home.
	if err := os.Remove(filepath.Join(home, "secrets.key")); err != nil {
		t.Fatal(err)
	}

	err = runSecretInit(io.Discard)
	if err == nil {
		t.Fatal("init generated a new key over established encryption — the old ciphertext is now unrecoverable")
	}
	for _, want := range []string{"recipient", "restore"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(home, "secrets.key")); !os.IsNotExist(statErr) {
		t.Error("refused init still wrote a key file")
	}
	now, _ := os.ReadFile(config.AuthPath())
	if string(now) != string(encrypted) {
		t.Error("refused init modified auth.json")
	}

	// And status names the state instead of claiming encryption is off.
	var out bytes.Buffer
	if err := runSecretStatus(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MISSING") {
		t.Fatalf("status does not flag the missing key:\n%s", out.String())
	}
	if strings.Contains(out.String(), "encryption off") {
		t.Fatalf("status claims encryption is off while ciphertext is on disk:\n%s", out.String())
	}
}

func TestSecretMigrateWithoutKeyPointsAtInit(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	err := runSecretMigrate(io.Discard)
	if err == nil {
		t.Fatal("migrate with no key must fail")
	}
	if !strings.Contains(err.Error(), "terva secret init") {
		t.Fatalf("error does not point at init: %v", err)
	}
}

func TestSecretStatusReportsShapeOnly(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-status-1"); err != nil {
		t.Fatal(err)
	}

	var before bytes.Buffer
	if err := runSecretStatus(&before); err != nil {
		t.Fatal(err)
	}
	out := before.String()
	if !strings.Contains(out, statePlaintext) || !strings.Contains(out, "encryption off") {
		t.Fatalf("pre-init status wrong:\n%s", out)
	}
	if strings.Contains(out, "sk-status-1") {
		t.Fatalf("status leaked a secret value:\n%s", out)
	}

	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	var after bytes.Buffer
	if err := runSecretStatus(&after); err != nil {
		t.Fatal(err)
	}
	out = after.String()
	if !strings.Contains(out, stateEncrypted) || !strings.Contains(out, "owner-only") || !strings.Contains(out, "age1") {
		t.Fatalf("post-init status wrong:\n%s", out)
	}
	if strings.Contains(out, "sk-status-1") || strings.Contains(out, "AGE-SECRET-KEY") {
		t.Fatalf("status leaked secret material:\n%s", out)
	}
}

func TestSecretCommandRouter(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if handled, _ := runSecretCommand([]string{"nope"}); handled {
		t.Fatal("non-secret argv handled")
	}
	handled, err := runSecretCommand([]string{"secret", "bogus"})
	if !handled || err == nil {
		t.Fatal("unknown subcommand must be handled and fail")
	}
	if handled, err := runSecretCommand([]string{"secret", "status"}); !handled || err != nil {
		t.Fatalf("status route failed: handled=%v err=%v", handled, err)
	}
}

// A bot token still sitting in bot.json must be swept into the store by
// migrate, and reported by status until it is.
//
// This is the defect a fully green suite missed and the binary found: the
// conversion happened only on the next SaveConfig, which a user has no reason
// to trigger, so a plaintext credential would sit indefinitely in a home whose
// status said everything was encrypted.
func TestMigrateSweepsALegacyBotToken(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	legacy := `{"bot_token":"123456:legacy-secret","bot_username":"oldbot","allowed_user_id":7}`
	if err := os.WriteFile(filepath.Join(home, "bot.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before: status names it, so the state is visible rather than silent.
	var before strings.Builder
	if err := runSecretStatus(&before); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before.String(), "bot.json") || !strings.Contains(before.String(), "PLAINTEXT") {
		t.Fatalf("status did not flag the plaintext bot token:\n%s", before.String())
	}

	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "bot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-secret") {
		t.Fatal("the token is still in bot.json after migrate")
	}
	if !strings.Contains(string(raw), "oldbot") {
		t.Error("migrate lost the rest of bot.json")
	}

	// The token is intact, and now sealed.
	c, err := telegram.LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.BotToken != "123456:legacy-secret" {
		t.Fatalf("the token did not survive the move: %q", c.BotToken)
	}
	storeRaw, err := os.ReadFile(config.SecretStorePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(storeRaw) {
		t.Fatal("the store holding the token is not encrypted")
	}

	var after strings.Builder
	if err := runSecretStatus(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after.String(), "PLAINTEXT bot token") {
		t.Errorf("status still reports a plaintext token:\n%s", after.String())
	}
	if !strings.Contains(after.String(), "core:bot.telegram") {
		t.Errorf("status does not report the stored scope:\n%s", after.String())
	}
}

// Rotation must move the store onto the new key along with everything else —
// the store is whole-file sealed, so a rotation that skipped it would leave the
// bot token readable only by the retired key.
func TestRotateResealsTheSecretStore(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := runSecretInit(io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := telegram.SaveConfig(home, telegram.Config{BotToken: "123456:tg-secret"}); err != nil {
		t.Fatal(err)
	}

	if err := runSecretRotate([]string{"--revoke"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	// The current key (the new one) opens it, and the value survived.
	c, err := telegram.LoadConfig(home)
	if err != nil {
		t.Fatalf("the store did not survive rotation: %v", err)
	}
	if c.BotToken != "123456:tg-secret" {
		t.Fatalf("token lost across rotation: %q", c.BotToken)
	}
}
