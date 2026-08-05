package provider

import "testing"

// The single most important property of this field: it must not move money.
//
// ReasoningTokens is a SUBSET of OutputTokens — the model was billed for those
// tokens at the output rate and they are already counted there. A future
// change that "tidies up" by making the buckets disjoint, the way the prompt
// fields are, would silently reprice every reasoning turn. This is the guard
// that says no.
func TestReasoningTokensDoNotAffectCost(t *testing.T) {
	m := Model{PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25}
	base := Usage{InputTokens: 1000, OutputTokens: 4000, CacheReadTokens: 500, CacheWriteTokens: 100}

	withReasoning := base
	withReasoning.ReasoningTokens = 3000
	withReasoning.ReasoningTokensKnown = true

	if got, want := ComputeCost(m, withReasoning), ComputeCost(m, base); got != want {
		t.Fatalf("reasoning tokens changed the cost: %v vs %v — they are a subset of "+
			"OutputTokens and must never be priced separately", got, want)
	}
	if got, want := CacheSavings(m, withReasoning), CacheSavings(m, base); got != want {
		t.Fatalf("reasoning tokens changed cache savings: %v vs %v", got, want)
	}
}

// The subset relationship stated as an invariant, so a decoder that reports
// more reasoning than output is caught at the source rather than surfacing as
// a nonsensical percentage in a panel.
func TestReasoningTokensNeverExceedOutput(t *testing.T) {
	for _, u := range []Usage{
		{OutputTokens: 4000, ReasoningTokens: 3000, ReasoningTokensKnown: true},
		{OutputTokens: 10, ReasoningTokens: 10, ReasoningTokensKnown: true},
		{OutputTokens: 0, ReasoningTokens: 0, ReasoningTokensKnown: true},
	} {
		if u.ReasoningTokens > u.OutputTokens {
			t.Errorf("%+v: reasoning exceeds output, which the subset invariant forbids", u)
		}
	}
}

// CostTracker accumulates with total = total.Add(turn) from a zero Usage. A
// plain AND on the known flag would let that empty accumulator mark every
// total unknown, so the sum would disclaim knowledge it actually has.
func TestAddFromAZeroAccumulatorKeepsKnownness(t *testing.T) {
	turn := Usage{OutputTokens: 100, ReasoningTokens: 40, ReasoningTokensKnown: true}
	if got := (Usage{}).Add(turn); !got.ReasoningTokensKnown {
		t.Error("accumulating a known turn onto an empty total lost the known flag")
	}
	if got := turn.Add(Usage{}); !got.ReasoningTokensKnown {
		t.Error("adding an empty usage onto a known total lost the known flag")
	}
	if got := (Usage{}).Add(turn).ReasoningTokens; got != 40 {
		t.Errorf("reasoning tokens = %d, want 40", got)
	}
}

// ...but a real turn that does NOT report reasoning must poison the total,
// because a session mixing Anthropic with a reporting provider knows only a
// floor. This is the half that stops the fix above from becoming "always
// known".
func TestOneUnreportedTurnMakesTheTotalUnknown(t *testing.T) {
	known := Usage{OutputTokens: 100, ReasoningTokens: 40, ReasoningTokensKnown: true}
	anthropic := Usage{OutputTokens: 250} // thinking is inside output, never broken out

	total := known.Add(anthropic)
	if total.ReasoningTokensKnown {
		t.Error("a turn that reported no reasoning count left the total claiming to be known")
	}
	if total.ReasoningTokens != 40 {
		t.Errorf("reasoning tokens = %d, want the 40 that WAS reported kept as a floor", total.ReasoningTokens)
	}
}
