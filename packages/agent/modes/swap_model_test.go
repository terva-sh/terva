package modes

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// newInteractiveForSwapTest builds the minimal Interactive that swapModel
// touches, pointed at an openai-compatible model named curModel.
func newInteractiveForSwapTest(curModel string) (*Interactive, *int) {
	built := 0
	iv := &Interactive{
		view:  &tui.View{},
		turns: newTurnEngine(),
	}
	iv.turns.SetAgent(&core.Agent{Model: curModel})
	iv.cfg.Provider = "openai-compatible"
	iv.cfg.Model = curModel
	iv.cfg.BuildAgentFor = func(prov, model string) (*core.Agent, string, string, error) {
		built++
		return &core.Agent{Model: model}, prov, model, nil
	}
	return iv, &built
}

// TestSwapModelRebuildsWhenBaseURLChanges is the regression for the
// wrong-backend bug: switching to a same-provider model whose models.json
// baseUrl points at a different endpoint must rebuild the agent (and thus
// its endpoint-bound client + the terva_status tool), not mutate the model
// in place on the old client.
func TestSwapModelRebuildsWhenBaseURLChanges(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "edge-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://a.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "edge-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://b.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	iv, built := newInteractiveForSwapTest("edge-a")
	iv.applyModelSelection("", "edge-b")

	if *built != 1 {
		t.Fatalf("swap to a different-baseURL model must rebuild the agent; builder calls = %d", *built)
	}
	if iv.cfg.Model != "edge-b" {
		t.Fatalf("cfg.Model = %q, want edge-b", iv.cfg.Model)
	}
	if iv.turns.Agent().Model != "edge-b" {
		t.Fatalf("rebuilt agent Model = %q, want edge-b", iv.turns.Agent().Model)
	}
}

// TestSwapModelReusesClientWhenBaseURLUnchanged keeps the fast path: two
// models on the same endpoint (here, the shared default — no per-model
// baseUrl) swap in place with no rebuild, so the cheap path still works.
func TestSwapModelReusesClientWhenBaseURLUnchanged(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "same-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "same-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	iv, built := newInteractiveForSwapTest("same-a")
	prevAgent := iv.turns.Agent()
	iv.applyModelSelection("", "same-b")

	if *built != 0 {
		t.Fatalf("same-endpoint swap must not rebuild; builder calls = %d", *built)
	}
	if iv.turns.Agent() != prevAgent {
		t.Fatal("same-endpoint swap replaced the agent; the client should be reused")
	}
	if iv.turns.Agent().Model != "same-b" {
		t.Fatalf("in-place agent Model = %q, want same-b", iv.turns.Agent().Model)
	}
}
