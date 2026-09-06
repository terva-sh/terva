package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A model that exists only in models.json has to reach the picker saying so.
// Source tells a client the entry changed something, and Custom tells it that
// removing the entry removes the model rather than restoring a default. Without
// both on the wire the web picker cannot highlight the row or word its own
// delete button, which is the whole reason the fields exist.
//
// Seeded through LoadUserModelsWithWarnings rather than SetUserModels: the
// loader is what stamps Source on an entry, so the convenience helper would
// seed a state production never produces (Custom set, Source empty) and the
// assertion below would be meaningless.
func TestModelsReportsAModelsJSONOnlyModelAsUserSourcedAndCustom(t *testing.T) {
	seedCreds(t, "")
	// A keyless named endpoint, so the provider passes the credential filter
	// in Models() without this test needing an API key.
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"workshop":{"models":[
		{"id":"invented-local","contextWindow":262144,"maxTokens":8192}
	]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := provider.LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected models.json warnings: %v", warnings)
	}
	provider.SetUserOverrides(overrides)

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	for _, m := range res.Models {
		if m.Provider != "workshop" || m.ID != "invented-local" {
			continue
		}
		if m.Source != "user" {
			t.Errorf("source = %q, want \"user\"; the picker cannot mark the row as overridden", m.Source)
		}
		if !m.Custom {
			t.Error("custom is false for a model with nothing underneath it; " +
				"a client would offer to restore defaults that do not exist")
		}
		return
	}
	t.Fatalf("the models.json-only model never reached the picker: %d models listed", len(res.Models))
}
