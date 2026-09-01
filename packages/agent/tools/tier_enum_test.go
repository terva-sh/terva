package tools

import (
	"encoding/json"
	"slices"
	"testing"
)

// Both spawn tools advertise the tier ladder to the model as a schema enum, and
// both spelled it out as a literal. A literal drifts silently in the direction
// that matters: resolveSpawnRoute validates nothing, so a rung the ladder has
// and the enum omits still WORKS — the model is simply never told it may ask.
// That is how `cheap` reached actor_spawn as a tier the resolver honoured and
// the schema denied.
//
// The ladder is the authority for both.
func TestSpawnSchemasOfferEveryLadderTier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema json.RawMessage
	}{
		{"swarm_spawn", (&SwarmSpawnTool{}).Schema()},
		{"actor_spawn", (&ActorSpawnTool{Cast: map[string]CastMember{"a": {}}}).Schema()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal(tc.schema, &schema); err != nil {
				t.Fatalf("schema is not valid JSON: %v", err)
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatal("schema has no properties object")
			}
			tier, ok := props["tier"].(map[string]any)
			if !ok {
				t.Fatal("schema has no tier property")
			}
			raw, ok := tier["enum"].([]any)
			if !ok {
				t.Fatal("tier is a free string — the model gets no list of tiers at all")
			}
			var got []string
			for _, e := range raw {
				got = append(got, e.(string))
			}
			want := slices.Clone(SwarmTierNames())
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("tier enum = %v, the ladder is %v", got, want)
			}
		})
	}
}
