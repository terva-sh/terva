package agent

import (
	"testing"

	"terva.sh/terva/packages/provider"
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
	a, err := ParseArgs([]string{"--list-models"})
	if err != nil || !a.ListModels || a.ListModelsFilter != "" {
		t.Fatalf("bare --list-models: %+v err=%v", a, err)
	}
	a, err = ParseArgs([]string{"--list-models=available,live+"})
	if err != nil || !a.ListModels || a.ListModelsFilter != "available,live+" {
		t.Fatalf("--list-models=...: %+v err=%v", a, err)
	}
}
