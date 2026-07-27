package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// TW-039. One workflow run spent $24.4936 while its launching session's record
// ended at $5.3602 — 16% of what the task actually cost. Nothing was missing
// from disk; the number was computed and discarded at the process boundary.
//
// Delegated spend is a SUBSET of the total, not an addition. Both halves matter:
// a total that excludes it understates what the session caused, and a total that
// merges it hides which part a coordinator chose to spend.
func TestDelegatedUsageIsInsideTheTotalAndSeparatelyReadable(t *testing.T) {
	a := &Agent{}
	a.cost.Add(provider.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 5.36})
	a.RecordDelegatedUsage(provider.Usage{InputTokens: 900, OutputTokens: 300, CostUSD: 24.4936})

	total := a.Cost()
	if got, want := total.CostUSD, 29.8536; !nearly(got, want) {
		t.Errorf("total $%.4f, want $%.4f — delegated spend is not in the session's totals", got, want)
	}
	if got, want := a.DelegatedCost().CostUSD, 24.4936; !nearly(got, want) {
		t.Errorf("delegated $%.4f, want $%.4f", got, want)
	}
	if a.DelegatedCost().CostUSD > total.CostUSD {
		t.Error("delegated exceeds the total — it is being added alongside rather than folded in")
	}
	if got, want := total.InputTokens, 1000; got != want {
		t.Errorf("input tokens %d, want %d — a child's tokens were dropped", got, want)
	}
}

// The per-turn snapshot is the CONTEXT gauge. A sub-agent's prompt is not this
// session's context, so booking one must not move it — otherwise every
// compaction threshold reads a size the transcript never had.
func TestDelegatedUsageDoesNotDisturbTheContextGauge(t *testing.T) {
	a := &Agent{}
	a.cost.Add(provider.Usage{InputTokens: 100, OutputTokens: 20})
	before := a.LastTurnUsage()

	a.RecordDelegatedUsage(provider.Usage{InputTokens: 500_000, OutputTokens: 9_000, CostUSD: 24.49})

	if after := a.LastTurnUsage(); after != before {
		t.Errorf("last-turn snapshot moved from %+v to %+v — the context gauge now reads a size the transcript never had", before, after)
	}
}

// A zero booking is a no-op, so a child that reported nothing cannot fire an
// observer or perturb the totals.
func TestZeroDelegatedUsageIsIgnored(t *testing.T) {
	a := &Agent{}
	a.RecordDelegatedUsage(provider.Usage{})
	if a.Cost() != (provider.Usage{}) || a.DelegatedCost() != (provider.Usage{}) {
		t.Error("a zero booking changed the totals")
	}
}

func nearly(a, b float64) bool {
	d := a - b
	return d < 0.0001 && d > -0.0001
}
