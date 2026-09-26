package core

// The prefix watch and the cache-cliff detector are an experiment
// (packages/core/exp/prefixwatch), but the records they write are rows the
// session store reads back and session_inspect renders. A store must not depend
// on an experiment to read its own rows, so the types stay here, as the stall
// records do (stall_records.go).

// PrefixDivergence describes how one dispatch's prefix relates to the previous
// one's.
type PrefixDivergence struct {
	// Appended is the healthy case: every rung the two share is identical, and
	// the new request only added more. A provider re-reads nothing.
	Appended bool
	// Rung is the first index at which the two ladders differ; -1 when they did
	// not. Everything from here on is re-read at full price.
	Rung int
	// Label names that rung ("tools", "system", "message 12").
	Label string
	// MsgCount and PrevMsgCount give the divergence its scale.
	MsgCount, PrevMsgCount int
	// CachedTokens is what the previous request actually had cached, read from
	// its usage row rather than estimated — the bill this divergence causes.
	CachedTokens int
}

// Mutation reports whether the transcript was REBUILT rather than extended: a
// divergence at a rung that both requests share. This is the expensive,
// invisible case — the same conversation re-rendered into different bytes — as
// opposed to a deliberate, explicable invalidation like a model swap.
func (d PrefixDivergence) Mutation() bool {
	return !d.Appended && d.Rung >= 0
}

// CacheCliff is what a cache-cliff detector reports: a run of consecutive
// dispatches whose cache read collapsed while the prompt kept growing, which is
// how a provider-side cache outage shows from inside a session. The detector is
// packages/core/exp/prefixwatch.
type CacheCliff struct {
	// Dispatches is how many consecutive dispatches collapsed so far. On a
	// voided ending this is a lower bound: the run was cut short as a
	// measurement, not as a collapse.
	Dispatches int
	// RereadTokens is the input the provider re-read across the run that the
	// previous dispatch's prompt already covered — the waste, not the bill.
	RereadTokens int
	// Ongoing is true while the run continues. The end-of-run event carries
	// false, zero counts, and an End naming which ending it was.
	Ongoing bool
	// End is CliffEndNone while the run is ongoing, and names the ending on
	// the event that closes it.
	End CliffEnd
}

// CliffEnd names why a run stopped. A run the provider ended by serving the
// prefix again and a run terva stopped being able to measure are different
// facts, and reporting both as "closed" hid a real one: an activate_tools
// voids the baseline, so a 46-dispatch floor was recorded as three short runs
// that each read as a recovery. See section 15 of
// docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md.
type CliffEnd string

const (
	// CliffEndNone rides every ongoing event. It also rides a closing row
	// written before the two endings were told apart, so on a closed run it
	// means the reason is unrecorded and never that the cache recovered.
	CliffEndNone CliffEnd = ""
	// CliffEndRecovered is the run genuinely over: the next dispatch did not
	// meet the collapse test, so the totals are final.
	CliffEndRecovered CliffEnd = "recovered"
	// CliffEndVoided is the detector standing down rather than the collapse
	// ending. terva rebuilt the prefix, so the baseline the run was measured
	// against is gone. The collapse may continue, and the next run counts from
	// zero, which makes a voided run's length a floor and not a total.
	CliffEndVoided CliffEnd = "voided"
)
