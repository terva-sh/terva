package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/modes/telegram"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
	"terva.sh/terva/packages/testsupport"
)

// installHostKey gives the temp home an at-rest key and records its public half
// in config.json, which is what `terva secret init` does and the only thing a
// connector is ever supposed to read out of terva's home.
func installHostKey(t *testing.T, home string) *age.X25519Identity {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("# terva at-rest key\n# public key: %s\n%s\n", id.Recipient(), id)
	if err := os.WriteFile(filepath.Join(home, secrets.KeyFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"secrets":{"recipient":%q}}`, id.Recipient().String())
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

// newHome pins TERVA_HOME at a temp dir. Every path in this binary — the state
// dir, the host recipient, the key — hangs off envcompat.Home(), so a test that
// forgot this would read the DEVELOPER's home and assert against their real
// connector state.
func newHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}

// The invariant this connector exists to demonstrate: it holds its OWN key, and
// terva's private key is none of its business.
//
// It used to reuse telegram.LoadConfig/SaveConfig against its own directory. The
// directory governed the FILE and never the KEY: those functions build the store
// with config.SecretsCodec(), which resolves secrets.IdentityIn(CredentialHome())
// — the ambient home — so this standalone binary was a full holder of terva's
// at-rest key. Anything that compromised the connector could open auth.json and
// every other component's sealed state.
//
// The teeth are the rotation. Replacing the host key is what `terva secret
// rotate --revoke` does, and it is the step that separates "the connector has
// its own key" from "the connector borrows terva's": before this change the
// load below failed with "identity did not match any of the recipients" and the
// connector's own credential was unrecoverable.
func TestTheConnectorOpensItsTokenAfterTheHostKeyIsReplaced(t *testing.T) {
	home := newHome(t)
	installHostKey(t, home)

	const token = "8100000000:AAH-not-a-real-bot-token-000000000000"
	want := telegram.Config{
		BotToken:      token,
		BotUsername:   "terva_test_bot",
		BotID:         8100000000,
		AllowedUserID: 4242,
		LastUpdateID:  99,
	}
	if err := saveConfig(want); err != nil {
		t.Fatal(err)
	}

	// The connector minted a key of its OWN. Without this the rotation below
	// could pass by the connector simply having no encryption at all.
	if _, err := os.Stat(state.KeyPath()); err != nil {
		t.Fatalf("the connector did not mint its own key at %s: %v", state.KeyPath(), err)
	}

	// And the token is genuinely sealed rather than sitting in the clear.
	raw, err := os.ReadFile(state.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("the bot token is plaintext on disk:\n%s", raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if s, _ := doc[tokenField].(string); !secrets.IsEncryptedField(s) {
		t.Fatalf("%s does not hold a sealed value: %q", tokenField, s)
	}

	// Rotate the host: a brand new terva key, and the recipient in config.json
	// replaced with it. The connector's own key is untouched.
	installHostKey(t, home)

	got, err := loadConfig()
	if err != nil {
		t.Fatalf("the connector cannot open its own token after a host key rotation: %v", err)
	}
	if got.BotToken != token {
		t.Errorf("bot token = %q, want %q", got.BotToken, token)
	}
	if got != want {
		t.Errorf("config did not round-trip:\n got %+v\nwant %+v", got, want)
	}
}

// The document shape is the contract with terva: the component registry records
// state.Paths, and secrets.Envelope seals exactly those pointers. A token
// written under any other key would be a token the seal never covers — plaintext
// in a file everything downstream calls clean.
func TestTheTokenLandsAtTheDeclaredPointer(t *testing.T) {
	newHome(t)
	doc, err := configDoc(telegram.Config{BotToken: "tok", BotID: 5})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	if len(state.Paths) != 1 {
		t.Fatalf("this test assumes one declared path, got %v", state.Paths)
	}
	key := strings.TrimPrefix(state.Paths[0], "/")
	if m[key] != "tok" {
		t.Errorf("the token is not at the declared pointer %s: %v", state.Paths[0], m)
	}
}

// configDoc goes through a generic map precisely so a field added to
// telegram.Config needs no second declaration here. This pins that: it fails if
// someone replaces the map round-trip with a hand-listed struct and forgets a
// field, which is how the in-tree and connector copies drifted the first time.
func TestEveryConfigFieldSurvivesTheDocumentRoundTrip(t *testing.T) {
	newHome(t)
	want := telegram.Config{
		BotToken:      "tok",
		BotUsername:   "someone",
		BotID:         11,
		AllowedUserID: 22,
		LastUpdateID:  33,
	}
	doc, err := configDoc(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := configFromDoc(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("round trip lost a field:\n got %+v\nwant %+v", got, want)
	}
	// LegacyToken is the IN-TREE carrier for a token still sitting in bot.json.
	// It must never be written here: two keys claiming to be the token is how a
	// reader picks the stale one.
	if strings.Count(string(doc), `"bot_token"`) != 1 {
		t.Errorf("the document does not carry exactly one bot_token key: %s", doc)
	}
}

// A home written by the previous layout must come forward on its own. The old
// token was sealed to TERVA's key, so this is the one moment the connector is
// allowed to read it that way — and after it, the host-keyed files are gone.
func TestAPreSealedStateHomeMigratesItselfOnce(t *testing.T) {
	home := newHome(t)
	installHostKey(t, home)

	const token = "8100000000:AAH-legacy-token-0000000000000000000"
	legacy := telegram.Config{BotToken: token, BotUsername: "old_bot", BotID: 7, AllowedUserID: 1, LastUpdateID: 2}
	if err := telegram.SaveConfig(stateDir(), legacy); err != nil {
		t.Fatal(err)
	}
	oldStore := filepath.Join(stateDir(), secretstore.FileName)
	for _, p := range []string{telegram.ConfigPath(stateDir()), oldStore} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("precondition: the old layout was not written at %s: %v", p, err)
		}
	}

	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Errorf("migration lost state:\n got %+v\nwant %+v", got, legacy)
	}
	if _, err := os.Stat(state.Path()); err != nil {
		t.Fatalf("migration did not write the new config: %v", err)
	}
	for _, p := range []string{telegram.ConfigPath(stateDir()), oldStore} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the host-keyed file survived the migration: %s", p)
		}
	}

	// And the migrated token now opens without terva's key at all.
	installHostKey(t, home)
	after, err := loadConfig()
	if err != nil {
		t.Fatalf("the migrated token does not survive a host rotation: %v", err)
	}
	if after.BotToken != token {
		t.Errorf("bot token = %q, want %q", after.BotToken, token)
	}
}

// A host that has never run `terva secret init` must still be configurable —
// SealedState writes plaintext in that case, on purpose, because refusing would
// make encryption a prerequisite for using a chat connector.
func TestAHomeWithNoAtRestKeyStillConfigures(t *testing.T) {
	newHome(t)
	const token = "8100000000:AAH-unencrypted-home-00000000000000"
	if err := saveConfig(telegram.Config{BotToken: token, BotID: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != token {
		t.Errorf("bot token = %q, want %q", got.BotToken, token)
	}
}

// stateDir must be the directory the SDK and the host both name, or terva
// records a registry entry pointing at a file that does not exist.
func TestStateDirIsTheSDKStateDir(t *testing.T) {
	newHome(t)
	if got, want := stateDir(), connsdk.StateDir(name); got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
	if got, want := state.RelPath(), filepath.Join("connectors", name, "config.json"); got != want {
		t.Errorf("RelPath() = %q, want %q — the component registry hardcodes this shape", got, want)
	}
}
