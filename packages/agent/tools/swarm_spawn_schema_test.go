package tools

import (
	"encoding/json"
	"testing"
)

// The dispatchable persona names ride the schema as the `persona` enum (the
// shrink moved them off the system prompt), so the model can only pick a real
// specialist and gets validation for free; with none, persona stays free-form.
func TestSwarmSpawnPersonaEnum(t *testing.T) {
	personaEnum := func(raw json.RawMessage) ([]any, bool) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("schema not valid json: %v", err)
		}
		p := m["properties"].(map[string]any)["persona"].(map[string]any)
		e, ok := p["enum"].([]any)
		return e, ok
	}

	if _, ok := personaEnum((&SwarmSpawnTool{}).Schema()); ok {
		t.Error("no personas should leave the persona argument a free string (no enum)")
	}

	e, ok := personaEnum((&SwarmSpawnTool{Personas: []string{"sec-reviewer", "test-writer"}}).Schema())
	if !ok || len(e) != 2 || e[0] != "sec-reviewer" || e[1] != "test-writer" {
		t.Errorf("persona enum = %v (present=%v), want [sec-reviewer test-writer]", e, ok)
	}
}
