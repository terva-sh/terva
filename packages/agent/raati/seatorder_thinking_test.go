package raati

import "testing"

func pool(bs ...Binding) []Binding { return bs }
func bind(model, reasoning string) Binding {
	return Binding{Provider: "p", Model: model, Reasoning: reasoning}
}

// A panel whose seats share weights and differ only in thinking effort has
// nothing but the effort telling the seats apart, so holding the effort fixed
// for a whole deliberation fuses "the benevolence seat" with "the one that
// wasn't thinking". Rotating per turn is what keeps the effort a property of
// the round rather than of the prior.
func TestSeatOrderDefaultsToTurnForAThinkingLadder(t *testing.T) {
	thinking := pool(bind("k3", "off"), bind("k3", "medium"), bind("k3", "high"))

	got, err := SeatOrderFor("", thinking)
	if err != nil {
		t.Fatalf("SeatOrderFor: %v", err)
	}
	if got != SeatOrderTurn {
		t.Errorf("default for a thinking ladder = %q, want turn", got)
	}

	// Only the DEFAULT moves. `turn` respawns every seat cold in round two —
	// no prompt-cache reuse, question and evidence re-read per seat per
	// round — and refusing that trade is the operator's call to make.
	for _, explicit := range []string{"convene", "fixed", "turn"} {
		got, err := SeatOrderFor(explicit, thinking)
		if err != nil {
			t.Fatalf("SeatOrderFor(%q): %v", explicit, err)
		}
		if string(got) != explicit {
			t.Errorf("explicit %q became %q", explicit, got)
		}
	}
}

func TestSeatOrderDefaultUnchangedForEveryOtherPanel(t *testing.T) {
	cases := map[string][]Binding{
		"a cross-model ladder": pool(bind("a", ""), bind("b", ""), bind("c", "")),
		"identical seats":      pool(bind("a", ""), bind("a", ""), bind("a", "")),
		// Same weights AND same effort: correlated, not a ladder — nothing
		// to rotate, so nothing to pay for rotating.
		"one model at one effort": pool(bind("a", "high"), bind("a", "high")),
		"a single seat":           pool(bind("a", "high")),
		"no seats":                nil,
	}
	for name, p := range cases {
		got, err := SeatOrderFor("", p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != SeatOrderConvene {
			t.Errorf("%s default = %q, want convene", name, got)
		}
	}
}

func TestSeatOrderForStillRejectsGarbage(t *testing.T) {
	if _, err := SeatOrderFor("sideways", nil); err == nil {
		t.Error("an unknown seat order must still error")
	}
}

func TestSameWeightsAndSameEffort(t *testing.T) {
	if !SameWeights(nil) || !SameEffort(nil) {
		t.Error("an empty pool has nothing to disagree with")
	}
	mixed := pool(bind("a", "high"), bind("b", "high"))
	if SameWeights(mixed) {
		t.Error("different models are different weights")
	}
	if !SameEffort(mixed) {
		t.Error("same effort on different models is still same effort")
	}
	ladder := pool(bind("a", "off"), bind("a", "high"))
	if !SameWeights(ladder) || SameEffort(ladder) {
		t.Error("a thinking ladder is same weights, different effort")
	}
}
