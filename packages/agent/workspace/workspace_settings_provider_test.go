package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// providerSession builds a session bound to one provider, so the pane's
// provider-scoped rows resolve against it.
func providerSession(t *testing.T, prov string) *wsSession {
	t.Helper()
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "s1", ws: w, hub: newWSHub(), provider: prov}
	w.sessions[s.id] = s
	s.agent = core.NewAgent(&gatedTurnClient{}, "m", "sys", core.Registry{})
	return s
}

// findItem returns the pane row with this key, and whether the pane carries it.
func findItem(s *wsSession, key string) (item ctrlproto.SettingItem, found bool) {
	for _, it := range s.settingsView().Items {
		if it.Key == key {
			return it, true
		}
	}
	return ctrlproto.SettingItem{}, false
}

// itemValue is findItem for the cases that only care what the row shows.
func itemValue(s *wsSession, key string) (string, bool) {
	it, ok := findItem(s, key)
	return it.Value, ok
}

// The row renders on every session, whatever provider it runs on. Hiding it
// off-codex made the setting findable only by already running the provider it
// configures, which is the wrong way round for a setting a user goes looking
// for. The scope lives in the note instead.
func TestCodexIdentityItemIsVisibleOnEveryProvider(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	for _, prov := range []string{"openai-codex", "anthropic", "openai", "google", ""} {
		if _, ok := findItem(providerSession(t, prov), "codex_client_identity"); !ok {
			t.Errorf("provider %q: the pane has no codex_client_identity row", prov)
		}
	}
}

// Visible everywhere is only safe while the note says where it bites. An
// identical note on every provider would let a user on anthropic read the row
// as governing the session in front of them, which is the misreading that
// hiding the row used to prevent.
func TestCodexIdentityNoteNamesItsScopeOffCodex(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	onCodex, ok := findItem(providerSession(t, "openai-codex"), "codex_client_identity")
	if !ok {
		t.Fatal("openai-codex session: no codex_client_identity row")
	}
	for _, prov := range []string{"anthropic", "openai", "google", ""} {
		off, ok := findItem(providerSession(t, prov), "codex_client_identity")
		if !ok {
			t.Fatalf("provider %q: no codex_client_identity row", prov)
		}
		if off.Note == onCodex.Note {
			t.Errorf("provider %q: the note is the same as on codex (%q); it must say the setting governs openai-codex", prov, off.Note)
		}
		if !strings.Contains(off.Note, "openai-codex") {
			t.Errorf("provider %q: the note %q never names openai-codex, so it does not say what the row governs", prov, off.Note)
		}
	}
}

// The write works from a session on another provider, which is the point of
// rendering the row there: a user on anthropic can set the identity their next
// codex session will start with. A visible row that refused to save would be
// worse than the hidden one it replaced.
func TestCodexIdentityWritesFromAnotherProvidersSession(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := providerSession(t, "anthropic")

	if err := s.settingsAction("set", map[string]string{"key": "codex_client_identity", "value": "native"}); err != nil {
		t.Fatalf("set native from an anthropic session: %v", err)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Providers["openai-codex"].ClientIdentity; got != "native" {
		t.Errorf("client_identity = %q; want %q (the write must reach openai-codex, not the session's own provider)", got, "native")
	}
	// The session's own provider gains nothing: this row names openai-codex and
	// writes openai-codex, wherever it was set from.
	if got := cfg.Providers["anthropic"].ClientIdentity; got != "" {
		t.Errorf("the anthropic provider gained client_identity %q from a codex row", got)
	}
}

// The pane reports the value it would write, default included. A row that
// showed nothing until the setting was non-default would leave a user unable
// to tell "terva" from "unset", which are the same thing here and should read
// as the same thing.
func TestCodexIdentityItemShowsTheDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	got, ok := itemValue(providerSession(t, "openai-codex"), "codex_client_identity")
	if !ok {
		t.Fatal("the pane has no codex_client_identity row")
	}
	if got != "" {
		t.Errorf("default value = %q; want %q (the terva identity)", got, "")
	}
}

// The write lands in the user layer. This is the security requirement of the
// setting and not a filing preference: ProviderSettings is absent from
// ProjectConfig so that a cloned repository cannot change the name terva
// presents to a provider. A write path that reached the project layer would
// undo that, and the guard on the struct would not catch it.
func TestCodexIdentityWriteLandsInTheUserLayer(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	s := providerSession(t, "openai-codex")

	if err := s.settingsAction("set", map[string]string{"key": "codex_client_identity", "value": "native"}); err != nil {
		t.Fatalf("set native: %v", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Providers["openai-codex"].ClientIdentity; got != "native" {
		t.Errorf("user config client_identity = %q; want %q", got, "native")
	}
	// The pane reads back what was written, so the row and the file agree.
	if got, _ := itemValue(s, "codex_client_identity"); got != "native" {
		t.Errorf("pane value after the write = %q; want %q", got, "native")
	}

	// And back to the default, because an opt-in a user cannot leave is not
	// opt-in. The empty string is the terva identity, so this also proves an
	// empty value is a real choice and not a no-op.
	if err := s.settingsAction("set", map[string]string{"key": "codex_client_identity", "value": ""}); err != nil {
		t.Fatalf("set terva: %v", err)
	}
	cfg, err = config.LoadConfig()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := cfg.Providers["openai-codex"].ClientIdentity; got != "" {
		t.Errorf("client_identity after reverting = %q; want %q", got, "")
	}
}

// An unknown word is refused rather than forwarded. The setting names a
// keyword that the provider maps to a header, so accepting an arbitrary string
// here would turn a settings row into a header-injection point.
func TestCodexIdentityRefusesAnUnknownValue(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	s := providerSession(t, "openai-codex")

	for _, bad := range []string{"codex_cli_rs", "CODEX", "native\r\nX-Injected: 1", "true"} {
		if err := s.settingsAction("set", map[string]string{"key": "codex_client_identity", "value": bad}); err == nil {
			t.Errorf("value %q was accepted; want a refusal", bad)
		}
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Providers["openai-codex"].ClientIdentity; got != "" {
		t.Errorf("a refused write still changed the config to %q", got)
	}
}
