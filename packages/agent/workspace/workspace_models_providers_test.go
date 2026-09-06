package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// 🪤 models.list carries the reachable provider set because it is a SUPERSET of
// the providers its own rows name, and the gap is the whole point.
//
// A provider the user is logged into that has no models yet contributes no row,
// so a client deriving the list from the rows drops exactly the provider an add
// form is most needed for: there is nothing under it to clone from, and pinning
// the add form's provider field to a clone source would make it unreachable.
//
// A named endpoint is the realistic version, and the one this checks: discovery
// has not run or returned nothing, so it is reachable and empty at once.
func TestModelsCarriesReachableProvidersThatHaveNoModels(t *testing.T) {
	seedCreds(t, `{"anthropic":{"api_key":"sk-a"}}`)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"empty-shop": {BaseURL: "http://127.0.0.1:9/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	named := map[string]bool{}
	for _, m := range res.Models {
		named[m.Provider] = true
	}
	reachable := map[string]bool{}
	for _, p := range res.Providers {
		reachable[p] = true
	}

	if len(res.Providers) == 0 {
		t.Fatal("no reachable providers crossed; the add form would have an empty picker")
	}
	if !reachable["empty-shop"] {
		t.Error("the reachable set omits a logged-in provider with no models, " +
			"which is the one case it exists to carry")
	}
	if named["empty-shop"] {
		t.Error("a provider with no models named a row; the premise of this test is wrong, not the code")
	}
	// Superset, not a replacement: every provider with a row must still be in it,
	// or the add form would refuse a provider the picker is offering.
	for p := range named {
		if !reachable[p] {
			t.Errorf("provider %q has rows but is absent from the reachable set", p)
		}
	}
}
