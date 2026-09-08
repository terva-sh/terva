package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The `reasoning` enum is rendered from provider.ReasoningLevels, never from a
// copy in the schema template. A hand-written copy is how "max" came to be
// accepted by the flag and advertised by nothing.
func TestSwarmSpawnReasoningEnumComesFromTheLadder(t *testing.T) {
	for _, tool := range []*SwarmSpawnTool{{}, {Personas: []string{"sec-reviewer"}}} {
		var m map[string]any
		if err := json.Unmarshal(tool.Schema(), &m); err != nil {
			t.Fatalf("schema not valid json: %v", err)
		}
		p, ok := m["properties"].(map[string]any)["reasoning"].(map[string]any)
		if !ok {
			t.Fatal("the schema should offer a reasoning argument")
		}
		var got []string
		for _, e := range p["enum"].([]any) {
			got = append(got, e.(string))
		}
		if !reflect.DeepEqual(got, provider.ReasoningLevels) {
			t.Errorf("reasoning enum = %v, want provider.ReasoningLevels %v", got, provider.ReasoningLevels)
		}
	}
}

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
