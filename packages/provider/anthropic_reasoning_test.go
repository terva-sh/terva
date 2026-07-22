package provider

import "testing"

// A model with a per-model DefaultReasoning ("high" on Kimi K3) emits a
// thinking block when the user set NO global level — the model default
// applies. The SAME model with the global level explicitly set to off
// (ReasoningSet=true, Reasoning="") emits NO thinking — the explicit user
// choice wins over the model default.
func TestBuildRequest_DefaultReasoningVsExplicitOff(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers() // resolve k3 from the baked-in catalog

	c := &anthropicClient{}

	// Unset global level: the model's DefaultReasoning:"high" drives a
	// budget-based thinking block.
	def, err := c.buildRequest(Request{
		Model:        "k3",
		Reasoning:    "",
		ReasoningSet: false,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if def.Thinking == nil {
		t.Fatal("model DefaultReasoning:\"high\" should emit a thinking block when the global level is unset")
	}
	if def.Thinking.Type != "enabled" {
		t.Errorf("thinking type = %q, want enabled", def.Thinking.Type)
	}
	if def.Thinking.BudgetTokens <= 0 {
		t.Errorf("budget_tokens = %d, want > 0", def.Thinking.BudgetTokens)
	}

	// Explicit global off (ReasoningSet with an empty level) wins over the
	// model default: no thinking block.
	off, err := c.buildRequest(Request{
		Model:        "k3",
		Reasoning:    "",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.Thinking != nil {
		t.Errorf("explicit global off must suppress thinking, got %+v", off.Thinking)
	}
}
