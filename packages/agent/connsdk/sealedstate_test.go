package connsdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// hostHome pins TERVA_HOME to a fresh dir whose config.json records terva's
// PUBLIC recipient, the way a host looks after `terva secret init`. The private
// key is returned but never written where the connector could reach it — the
// point is that the connector seals to terva without ever holding terva's key.
func hostHome(t *testing.T) *age.X25519Identity {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"secrets": map[string]string{"recipient": id.Recipient().String()}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func testState() SealedState {
	return SealedState{Name: "discord-ext", Paths: []string{"/bot_token"}}
}

// The two-party property, which is the whole point of this step: the connector
// seals with its own key, and TERVA can open the value — while the connector
// never holds terva's private key and terva never holds the connector's.
func TestBothPartiesOpenTheSecretNeitherHoldsTheOthersKey(t *testing.T) {
	terva := hostHome(t)
	s := testState()

	doc := []byte(`{"bot_token":"dc-live-token","allowed_user_id":"7"}`)
	if err := s.Save(doc); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dc-live-token") {
		t.Fatal("the bot token is readable on disk")
	}
	// Only the declared value was sealed — the rest stays inspectable, which is
	// why this is per-value and not whole-file.
	if !strings.Contains(string(raw), `"allowed_user_id": "7"`) {
		t.Errorf("non-secret state was hidden too:\n%s", raw)
	}
	if secrets.IsAgeFile(raw) {
		t.Error("the file was sealed whole; terva would be able to read the connector's entire state")
	}

	// The connector opens it with its own key.
	back, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		BotToken      string `json:"bot_token"`
		AllowedUserID string `json:"allowed_user_id"`
	}
	if err := json.Unmarshal(back, &got); err != nil {
		t.Fatal(err)
	}
	if got.BotToken != "dc-live-token" || got.AllowedUserID != "7" {
		t.Fatalf("connector round trip: %+v", got)
	}

	// And TERVA opens the same value with ITS key, which is what lets a
	// rotation or an audit run while this connector is not running.
	env := secrets.Envelope{Scope: "conn:discord-ext", Paths: []string{"/bot_token"}}
	opened, err := env.Open(raw, terva)
	if err != nil {
		t.Fatalf("terva could not open the connector's secret: %v", err)
	}
	if !strings.Contains(string(opened), "dc-live-token") {
		t.Fatal("terva opened the file but not the value")
	}

	// Neither key file is the other's.
	connKey, err := os.ReadFile(s.KeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(connKey), terva.String()) {
		t.Fatal("terva's private key was written into the connector's state dir")
	}
}

// A host that has never run `terva secret init` must still be able to configure
// a connector. Sealing degrades to plaintext there, exactly as before.
func TestNoHostRecipientWritesPlaintext(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := testState()

	if err := s.Save([]byte(`{"bot_token":"dc-plain"}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "dc-plain") {
		t.Fatal("the token was sealed with no host recipient available")
	}
	if _, err := os.Stat(s.KeyPath()); !os.IsNotExist(err) {
		t.Error("a connector key was minted with nothing to seal to")
	}
	back, err := s.Load()
	if err != nil || !strings.Contains(string(back), "dc-plain") {
		t.Fatalf("plaintext state did not load back: %s %v", back, err)
	}
}

// A file written before encryption was set up must keep working once it is, and
// convert on the next save — the same content-not-configuration rule the rest
// of this workstream uses.
func TestPlaintextConfigConvertsOnTheNextSave(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := testState()
	if err := s.Save([]byte(`{"bot_token":"dc-early"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Clean(); err == nil {
		t.Fatal("a plaintext token reported clean")
	}

	// The operator runs `terva secret init` afterwards.
	hostHome(t)
	// TERVA_HOME moved, so re-point the state dir by reloading from the old
	// one is not the case under test; write the same doc through Save again.
	if err := s.Save([]byte(`{"bot_token":"dc-early"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Clean(); err != nil {
		t.Fatalf("still not clean after a save with a recipient available: %v", err)
	}
}

// Clean is the whole reason paths are declared: it must fail on a plaintext
// declared value, and it must refuse to vouch for a file with no declaration at
// all — unknown is not clean.
func TestCleanNeedsADeclaration(t *testing.T) {
	hostHome(t)
	s := testState()
	if err := s.Save([]byte(`{"bot_token":"dc-live"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Clean(); err != nil {
		t.Fatalf("a sealed file is not clean: %v", err)
	}

	undeclared := SealedState{Name: "discord-ext"}
	if err := undeclared.Clean(); err == nil {
		t.Fatal("a file with no declared paths reported clean")
	}
}

// A secret moved to a different path in the same file must not open there —
// the binding from #599, exercised through a component file rather than
// config.json.
func TestAMovedSecretDoesNotOpen(t *testing.T) {
	hostHome(t)
	env := secrets.Envelope{Scope: "conn:discord-ext", Paths: []string{"/bot_token", "/other"}}
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := env.Seal([]byte(`{"bot_token":"dc-live","other":""}`), id.Recipient())
	if err != nil {
		t.Fatal(err)
	}

	// Relocate the sealed value to the other declared path.
	var m map[string]any
	if err := json.Unmarshal(sealed, &m); err != nil {
		t.Fatal(err)
	}
	m["other"] = m["bot_token"]
	m["bot_token"] = ""
	moved, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := env.Open(moved, id); err == nil {
		t.Fatal("a relocated secret opened at its new path")
	}
	// Both directions, or this passes for the wrong reason: a binding that
	// ignored the path entirely would also make this fail, by making
	// EVERYTHING fail. The untouched document must still open.
	opened, err := env.Open(sealed, id)
	if err != nil {
		t.Fatalf("the untouched document stopped opening: %v", err)
	}
	if !strings.Contains(string(opened), "dc-live") {
		t.Fatal("the value did not open at its own path")
	}
}

// Numbers must survive a round trip exactly. A connector's poll offset is an
// int64, and decoding through float64 corrupts large ones silently.
func TestLargeNumbersSurviveTheRoundTrip(t *testing.T) {
	hostHome(t)
	s := testState()
	const offset = "9007199254740993" // 2^53 + 1: the first int64 float64 cannot hold
	if err := s.Save([]byte(`{"bot_token":"t","last_update_id":` + offset + `}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), offset) {
		t.Fatalf("the offset was rewritten:\n%s", raw)
	}
}
