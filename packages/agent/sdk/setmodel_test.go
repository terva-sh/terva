package sdk

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// TestSetModelRejectsEndpointChange: switching to a same-provider model
// whose models.json baseUrl points at a different backend must be
// refused, not silently swapped onto the old endpoint-bound client. The
// SDK has no in-place rebuild — cross-endpoint switches are a fresh
// Runtime — so the guard returns an error rather than mis-route.
func TestSetModelRejectsEndpointChange(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "edge-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://a.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "edge-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://b.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	r := &Runtime{agent: &core.Agent{Model: "edge-a"}, provider: "openai-compatible", model: "edge-a"}
	if err := r.SetModel("edge-b"); err == nil {
		t.Fatal("expected an error switching to a different-endpoint model, got nil")
	}
	// The swap must not have partially applied.
	if r.model != "edge-a" || r.agent.Model != "edge-a" {
		t.Fatalf("rejected swap left state mutated: runtime=%q agent=%q", r.model, r.agent.Model)
	}
}

// TestSetModelInPlaceSameEndpoint keeps the fast path: two models on the
// same endpoint swap the id in place.
func TestSetModelInPlaceSameEndpoint(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "same-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "same-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	r := &Runtime{agent: &core.Agent{Model: "same-a"}, provider: "openai-compatible", model: "same-a"}
	if err := r.SetModel("same-b"); err != nil {
		t.Fatalf("same-endpoint swap should succeed: %v", err)
	}
	if r.model != "same-b" || r.agent.Model != "same-b" {
		t.Fatalf("in-place swap did not apply: runtime=%q agent=%q", r.model, r.agent.Model)
	}
}
