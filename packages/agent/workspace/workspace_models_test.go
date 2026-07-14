package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// The bug, from the operator's chair: they registered a named openai-compatible
// endpoint in the web UI and watched it appear in the provider pane — that pane
// reads config, so it saw the endpoint fine. Then the web's model picker showed
// them nothing, and they had to open the TUI to work with the models at all.
//
// Models() filtered the catalog through a credential check. A local server that
// wants no key has no credential to check, so every one of its models was dropped
// on the way to the picker. The provider pane and the model picker were asking two
// different questions and only one of them was the right one.
func TestModelsListsAKeylessNamedEndpointsModels(t *testing.T) {
	seedCreds(t, "")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider:      "workshop",
		ID:            "qwen3-coder",
		ContextWindow: 262144,
	}})

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	models, err := w.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	for _, m := range models {
		if m.Provider == "workshop" && m.ID == "qwen3-coder" {
			return
		}
	}
	t.Fatalf("the endpoint's model never reached the picker: %d models listed, none from workshop", len(models))
}
