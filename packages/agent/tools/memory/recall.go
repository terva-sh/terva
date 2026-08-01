package memory

import (
	"terva.sh/terva/packages/agent/lore"
)

// Retrieval tuning for the archived tier. All three are deliberate departures
// from lore's defaults, because a lorebook and a project memory sit on opposite
// sides of one property: in fiction the vocabulary is SHARED (the book names
// "the Accord" and the player says "the Accord"), while in a codebase the naming
// is asymmetric — the memory holds the cause and the user describes the symptom.
const (
	// recallScanDepth is how many recent user/assistant messages are scanned,
	// against lore's default of 2.
	//
	// Deeper because the measured failure of keyed memory is entirely
	// UNDER-firing: across five entries, fifteen questions and four spec sources,
	// false positives never exceeded 1 in 70 while recall ranged from 2/15 to
	// 15/15. When the error is that asymmetric, the cheap direction is more
	// scan. Note the scan window skips tool traffic, so 6 is roughly three
	// exchanges rather than three messages.
	recallScanDepth = 6

	// recallTokenBudget caps what the archived tier may add to one turn's tail,
	// in lore.ApproxTokens (bytes/4) — around ten typical entries.
	//
	// The number can be this relaxed because of what it is measured against: a
	// prefix invalidation on a long transcript costs ~$1.80 through the
	// provider's cache re-establishment window, while these bytes are ordinary
	// uncached input on the turns they fire. The budget is here to stop a
	// pathological spec dumping the whole archive into every turn, not to save
	// money at the margin.
	recallTokenBudget = 1200
)

// recallConfig is the matcher configuration for archived memory.
func recallConfig() lore.Config {
	return lore.Config{
		ScanDepth:   recallScanDepth,
		TokenBudget: recallTokenBudget,
		// Whole words, unlike lore's default. Memory keys are ordinary English —
		// "add", "model", "test" — and substring matching makes "add" fire on
		// "address" and "test" on "latest". Lore gets away with substrings
		// because its keys are proper nouns.
		MatchWholeWords: true,
		// No recursion. It is how lore builds multi-hop activation over an
		// implicit graph, and this tier has no graph yet: one memory's body
		// pulling in another on incidental shared vocabulary, with no weighting
		// to damp it, is a cascade rather than an association. The entries also
		// set PreventRecursion/ExcludeRecursion, so this stays off even if an
		// archive is ever matched alongside entries that want recursion.
		RecursiveScanning: false,
	}
}

// Recall runs one turn's retrieval over the archived tier and returns the block
// to append to the uncached per-turn tail, plus the trace of what fired.
//
// scan is the recent conversation, newest first. Both scopes are matched in ONE
// Select so they share a budget and a priority ordering: two archives with
// independent budgets would let a full user archive and a full project archive
// each spend the limit, and the whole point of a budget is that the turn has one.
//
// Pure, like the engine underneath it: no I/O, no clock. It reads each archive's
// current entries, so an archive/forget mid-session lands on the next turn with
// nothing to rebuild and no cached prompt disturbed.
func Recall(scan []string, archives ...*Archive) (block string, fired []RecallFired) {
	var entries []lore.Entry
	for _, a := range archives {
		if a == nil {
			continue
		}
		entries = append(entries, a.LoreEntries()...)
	}
	if len(entries) == 0 || len(scan) == 0 {
		return "", nil
	}
	res := lore.Select(entries, recallConfig(), scan, lore.ApproxTokens)

	var bodies []string
	for _, e := range res.All() {
		bodies = append(bodies, e.Content)
	}
	for _, f := range res.Fired {
		fired = append(fired, RecallFired{
			Ref:     f.Entry.Name,
			Keys:    f.Keys,
			Dropped: f.Dropped,
		})
	}
	return RenderRecalled(bodies), fired
}

// RecallFired is one archived entry that matched this turn: which entry, the
// trigger keys that fired it, and whether the tail budget cut it.
//
// The dropped ones matter as much as the kept ones. A spec whose entry fires and
// is then cut every turn looks identical, from the outside, to a spec that never
// fires at all — and the two need opposite fixes (lower the entry's competition
// vs. change its keys).
type RecallFired struct {
	Ref     string
	Keys    []string
	Dropped bool
}
