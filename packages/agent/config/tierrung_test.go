package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tier rung has two wire shapes and the bare string is the one every
// existing config already uses, so it has to keep working AND has to keep
// round-tripping back to a string — a config that never mentioned reasoning
// must not be rewritten into objects the first time terva saves it.
func TestTierRungWireShapes(t *testing.T) {
	var tc TierConfig
	raw := `{"weak": "gpt-5-nano", "medium": {"model": "k3", "reasoning": "low"}, "strong": {"reasoning": "high"}}`
	if err := json.Unmarshal([]byte(raw), &tc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tc.Weak.Model != "gpt-5-nano" || tc.Weak.Reasoning != "" {
		t.Errorf("bare string rung = %+v", tc.Weak)
	}
	if tc.Medium.Model != "k3" || tc.Medium.Reasoning != "low" {
		t.Errorf("object rung = %+v", tc.Medium)
	}
	// Effort without a model is legal: the resolver fills in the rung's
	// built-in model.
	if tc.Strong.Model != "" || tc.Strong.Reasoning != "high" {
		t.Errorf("effort-only rung = %+v", tc.Strong)
	}

	out, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"weak":"gpt-5-nano"`) {
		t.Errorf("a model-only rung must round-trip as a bare string, got %s", got)
	}
	if !strings.Contains(got, `"reasoning":"low"`) {
		t.Errorf("an effort must survive the round trip, got %s", got)
	}
}

// An unset rung is dropped rather than written as "" — otherwise saving a
// config would fill every provider's ladder with empty rungs that read like
// deliberate pins.
func TestTierRungOmitsUnset(t *testing.T) {
	out, err := json.Marshal(TierConfig{Weak: TierRung{Model: "x"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(out); got != `{"weak":"x"}` {
		t.Errorf("marshal = %s, want only the set rung", got)
	}
}

func TestTierRungRejectsGarbage(t *testing.T) {
	var r TierRung
	if err := r.UnmarshalJSON([]byte(`123`)); err == nil {
		t.Error("a number is neither a model id nor a rung object")
	}
	// Null and empty leave the zero rung rather than erroring: an absent
	// rung is normal, not a mistake.
	if err := r.UnmarshalJSON([]byte(`null`)); err != nil || !r.IsZero() {
		t.Errorf("null rung = (%+v, %v)", r, err)
	}
}
