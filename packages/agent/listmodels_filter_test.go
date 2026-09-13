package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func filterNames(t *testing.T, filter string, selectable func(string) bool, models []provider.Model) []string {
	t.Helper()
	keep, err := modelListFilter(filter, selectable)
	if err != nil {
		t.Fatalf("filter %q: %v", filter, err)
	}
	var out []string
	for _, m := range models {
		if keep(m) {
			out = append(out, m.ID)
		}
	}
	return out
}

func TestModelListFilter(t *testing.T) {
	models := []provider.Model{
		{ID: "mine", Provider: "anthropic", Source: "user"},
		{ID: "fresh", Provider: "anthropic", Source: "live"},
		{ID: "cached", Provider: "openai", Source: "cache"},
		{ID: "baked", Provider: "openai", Source: ""},                      // catalog
		{ID: "rumored", Provider: "openai", Source: "", Speculative: true}, // speculative wins
		{ID: "unauthed", Provider: "mistral", Source: "live"},              // provider without creds
	}
	allSelectable := func(string) bool { return true }
	onlyAnthropic := func(p string) bool { return p == "anthropic" }

	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	// Empty filter admits everything.
	eq(filterNames(t, "", allSelectable, models), "mine", "fresh", "cached", "baked", "rumored", "unauthed")
	// Exact user.
	eq(filterNames(t, "user", allSelectable, models), "mine")
	// Exact live folds in cache (yesterday's live) so the filter doesn't
	// go empty once the discovery cache takes over between runs.
	eq(filterNames(t, "live", allSelectable, models), "fresh", "cached", "unauthed")
	// cache alone is still addressable.
	eq(filterNames(t, "cache", allSelectable, models), "cached")
	// Tier thresholds: live+ = user/live/cache; catalog+ excludes only
	// speculative.
	eq(filterNames(t, "live+", allSelectable, models), "mine", "fresh", "cached", "unauthed")
	eq(filterNames(t, "catalog+", allSelectable, models), "mine", "fresh", "cached", "baked", "unauthed")
	// available consults the credential probe by provider.
	eq(filterNames(t, "available", onlyAnthropic, models), "mine", "fresh")
	// Terms AND together.
	eq(filterNames(t, "available,live+", onlyAnthropic, models), "mine", "fresh")

	// Unknown terms error, with and without the + form.
	if _, err := modelListFilter("bogus", allSelectable); err == nil {
		t.Error("unknown term should error")
	}
	if _, err := modelListFilter("bogus+", allSelectable); err == nil {
		t.Error("unknown tier should error")
	}
}

func TestParseArgsListModelsFilter(t *testing.T) {
	a, err := build.ParseArgs([]string{"--list-models"})
	if err != nil || !a.ListModels || a.ListModelsFilter != "" {
		t.Fatalf("bare --list-models: %+v err=%v", a, err)
	}
	a, err = build.ParseArgs([]string{"--list-models=available,live+"})
	if err != nil || !a.ListModels || a.ListModelsFilter != "available,live+" {
		t.Fatalf("--list-models=...: %+v err=%v", a, err)
	}
}

// --list-models=available must include a KEYLESS backend.
//
// 🪤 "Reachable" is not "has a credential". Most servers an operator points
// terva at want no key, so a predicate built on ResolveCredential reports the
// one backend they are certain to be able to reach as one they are signed out
// of — and `available`, the flag whose entire job is to say what you can run,
// was the surface that got it wrong.
//
// ollama and the shared compatible slots were special-cased by hand here while
// NAMED endpoints were not, so a keyless local endpoint appeared under a bare
// --list-models and vanished under --list-models=available. This pins the fix:
// the answer comes from build.LoggedInProviderSet, which already knew.
func TestProviderSelectableIncludesKeylessBackends(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"endpoints":{
		"keyless-box": {"baseUrl":"http://box.invalid:8000/v1"},
		"claude-gw":   {"baseUrl":"http://gw.invalid:4000","api":"anthropic"}
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	build.RegisterEndpointsFromConfig()
	for _, id := range []string{"keyless-box", "claude-gw"} {
		if err := build.RegisterOrReplaceEndpoint(id, mustEndpoint(t, id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		t.Cleanup(func() { build.UnregisterEndpoint(id) })
	}

	cache := map[string]bool{}
	for _, id := range []string{"ollama", provider.OpenAICompatProvider, provider.AnthropicCompatProvider} {
		if !providerSelectable(id, cache) {
			t.Errorf("providerSelectable(%q) = false; it needs no credential", id)
		}
	}
	for _, id := range []string{"keyless-box", "claude-gw"} {
		if !providerSelectable(id, cache) {
			t.Errorf("providerSelectable(%q) = false; a keyless named endpoint is the one backend the operator can certainly reach", id)
		}
	}
	// A real vendor with no credential is still unavailable — the filter has to
	// keep meaning something.
	if providerSelectable("mistral", cache) {
		t.Error("providerSelectable(mistral) = true with no credential stored")
	}
}

// mustEndpoint reads one endpoint back out of the config just written, so the
// test registers exactly what terva would at startup.
func mustEndpoint(t *testing.T, id string) config.EndpointConfig {
	t.Helper()
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := cfg.Endpoints[id]
	if !ok {
		t.Fatalf("endpoint %q missing from the config just written", id)
	}
	return ep
}
