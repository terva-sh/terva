package workspace

import (
	"context"
	"strings"
	"terva.sh/terva/packages/core/lazytools"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// activationFixture builds a workspace holding two live sessions on different
// providers, which is the only shape that can show the scoping actually scopes.
// A one-session fixture cannot tell "applied to that provider" from "applied to
// everything".
func activationFixture(t *testing.T) (*Workspace, *wsSession, *wsSession) {
	t.Helper()
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}

	newSession := func(id, prov string) *wsSession {
		s := &wsSession{id: id, ws: w, hub: newWSHub(), provider: prov, model: "m"}
		s.agent = coretest.NewAgent(&gatedTurnClient{}, "m", "sys", core.Registry{}, build.LazyTools(nil)...)
		w.sessions[id] = s
		return s
	}
	codex := newSession("codex-session", "openai-codex")
	other := newSession("other-session", "anthropic")

	on := true
	if err := config.MutateConfig(func(c *config.Config) { c.LazyTools = &on }); err != nil {
		t.Fatalf("seed lazy_tools: %v", err)
	}
	return w, codex, other
}

// The row reaches the settings surface, in the provider group, carrying the
// value for the provider THIS session runs on. Both clients render from this
// one declaration, so a row here is a row in /settings and in the web form.
func TestProviderActivationContinuationHasASettingsRow(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	_, codex, other := activationFixture(t)

	item, ok := settingsItem(codex.settingsView(), "provider_activation_continuation")
	if !ok {
		t.Fatal("the provider-scoped activation continuation must have a settings row")
	}
	if item.Group != "provider" {
		t.Errorf("row must land in the provider group, got %q", item.Group)
	}
	if item.Type != "enum" || len(item.Options) != 3 {
		t.Errorf("want a three-way enum (inherit/on/off), got type %q with %d options", item.Type, len(item.Options))
	}
	if item.Value != "" {
		t.Errorf("an unconfigured provider must read as inherit, got %q", item.Value)
	}
	// The description has to carry both halves: no cache-collapse remedy, and
	// the saving that is actually on offer. This is the criterion that stops the
	// row being sold as something it is not.
	if !strings.Contains(item.Description, "does NOT fix a sustained cache collapse") {
		t.Errorf("description must disclaim the cache-collapse remedy, got %q", item.Description)
	}
	if !strings.Contains(item.Description, "at most one request per activation") {
		t.Errorf("description must name the bounded saving, got %q", item.Description)
	}
	// The note names the wire being configured, because the row targets the
	// current provider rather than a fixed one.
	if !strings.Contains(item.Note, "openai-codex") {
		t.Errorf("note must name the provider this row governs, got %q", item.Note)
	}
	if n, _ := settingsItem(other.settingsView(), "provider_activation_continuation"); !strings.Contains(n.Note, "anthropic") {
		t.Errorf("a session on another provider must see its own provider named, got %q", n.Note)
	}
}

// A write persists under the session's provider and applies live to sessions on
// that provider ONLY. The second session is the control: if it moves too, the
// setting is a global wearing a provider's name.
func TestProviderActivationContinuationWriteIsScoped(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	_, codex, other := activationFixture(t)

	if err := codex.settingsAction("set", map[string]string{
		"key": "provider_activation_continuation", "value": "off",
	}); err != nil {
		t.Fatalf("set off: %v", err)
	}

	cfg, _ := config.LoadConfig()
	if got := cfg.Providers["openai-codex"].ActivationContinuation; got != "off" {
		t.Errorf("want the keyword persisted under openai-codex, got %q", got)
	}
	if got := cfg.Providers["anthropic"].ActivationContinuation; got != "" {
		t.Errorf("the write must not touch another provider, got %q", got)
	}
	if lazytools.Of(codex.agent).ContinuationEnabled() {
		t.Error("the session on openai-codex must go off live")
	}
	if !lazytools.Of(other.agent).ContinuationEnabled() {
		t.Error("the session on anthropic must be untouched — this is the whole point of scoping")
	}

	// Clearing back to inherit restores the global rather than leaving the last
	// value standing.
	if err := codex.settingsAction("set", map[string]string{
		"key": "provider_activation_continuation", "value": "",
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !lazytools.Of(codex.agent).ContinuationEnabled() {
		t.Error("clearing the override must restore the global, not keep the override's value")
	}
}

// A global toggle must not erase a provider override on a live agent.
// applyEngineFeature pushes one value onto every agent, which is right for the
// other features and wrong for this one, so the write re-resolves per session.
func TestGlobalToggleDoesNotClobberAProviderOverride(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	_, codex, other := activationFixture(t)

	// codex pins itself ON explicitly.
	if err := codex.settingsAction("set", map[string]string{
		"key": "provider_activation_continuation", "value": "on",
	}); err != nil {
		t.Fatalf("set on: %v", err)
	}
	// Then the global goes off.
	if err := other.settingsAction("set", map[string]string{
		"key": build.ActivationContinuationFeatureID, "value": "false",
	}); err != nil {
		t.Fatalf("set global off: %v", err)
	}

	if !lazytools.Of(codex.agent).ContinuationEnabled() {
		t.Error("a provider that pinned itself on must survive the global going off")
	}
	if lazytools.Of(other.agent).ContinuationEnabled() {
		t.Error("a provider with no override must follow the global off — the control for the assertion above")
	}
}

// A typo is refused at the boundary. Accepting it would write a word that
// silently inherits, so the user would see the row keep its old value with no
// error to explain why.
func TestProviderActivationContinuationRefusesAnUnknownKeyword(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	_, codex, _ := activationFixture(t)

	if err := codex.settingsAction("set", map[string]string{
		"key": "provider_activation_continuation", "value": "offf",
	}); err == nil {
		t.Fatal("an unknown keyword must be refused")
	}
	cfg, _ := config.LoadConfig()
	if got := cfg.Providers["openai-codex"].ActivationContinuation; got != "" {
		t.Errorf("a refused write must persist nothing, got %q", got)
	}
}
