package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A host with no model files runs a turn on models it passes in from memory.
// The wire reads no cache and no models.json (TKT-01M35WK03); a model the host
// hands to SetUserModels is what the engine budgets against, for the output
// limit on the request and for the context window.
func TestATurnRunsOnModelsTheHostPassesInWithNoFiles(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("HOME", testsupport.TempDir(t))
	provider.SetUserModels([]provider.Model{{
		Provider: "framecap", ID: "host-model", ContextWindow: 50_000, MaxOutput: 1234,
	}})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	client := &frameCaptureClient{}
	a := newTestAgent(client, "host-model", "system", Registry{})
	a.SetModel("host-model")
	if err := a.Prompt(context.Background(), "hi", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(client.reqs) != 1 || client.reqs[0].MaxTokens != 1234 {
		t.Fatalf("requests %d, MaxTokens %d; want one request budgeted at the host model's 1234", len(client.reqs), client.reqs[0].MaxTokens)
	}
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 25_000})
	if _, window := a.ContextUsage(); window != 50_000 {
		t.Errorf("context window %d, want the host model's 50000", window)
	}
}
