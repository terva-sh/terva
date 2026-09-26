package core

import "terva.sh/terva/packages/provider"

// UsageFold recovers, from the usage and compaction rows a transcript holds in
// order, the latest cumulative usage, the per-turn usage of the final completed
// turn, and the baseline a resuming host should seed the context gauge with.
// The JSONL session store (session.SessionUsageDetail) feeds it from a file and
// MemoryTranscriptStore from its appends, so a transcript resumes to the same
// figures whichever store kept it. The zero value is ready to use.
//
// Some historical sessions logged the per-turn `usage` field as a copy of
// `cumulative` instead of the true delta. To recover an accurate last-turn
// snapshot (used by the status-bar context gauge on resume), we always derive
// lastTurn from the delta between the final two cumulative rows. For
// prompt-size purposes, cache_read/cache_write reflect the most recent prompt
// directly, so we take those from the final cumulative row as-is rather than
// as a delta.
//
// Compaction rows carry their own spend (AppendCompaction), and it is folded
// into the running total in memory — so a turn's cumulative row already
// contains every compaction that preceded it. Two corrections follow, and both
// matter:
//
//   - A compaction BETWEEN the final two turns inflates the naive delta,
//     because cum_N = cum_{N-1} + compaction + u_N. Left uncorrected the
//     resumed context gauge reads roughly double (a compaction's input is
//     transcript-sized), and the first threshold check fires a spurious
//     auto-compact on an already-condensed transcript. Subtract it.
//   - A compaction AFTER the last turn is in no cumulative row at all — the
//     in-memory total has it, but nothing wrote it. Compact and then quit for
//     the day, which is an ordinary thing to do, and the spend vanished. Add
//     it.
//
// Old sessions have no usage on their compaction rows; both corrections are
// then zero and this degrades exactly to the previous behaviour.
type UsageFold struct {
	prevCum, cumulative provider.Usage
	haveCum             bool
	sinceLastTurn       provider.Usage // compaction spend after the newest usage row
	betweenLastTwo      provider.Usage
	// A compaction after the newest usage row SUPERSEDES lastTurn as the resume
	// baseline — see Result. Tracked as a flag beside the estimate because
	// /clear writes AppendCompaction(nil), whose estimate is legitimately 0:
	// "the transcript is empty now" and "no compaction happened" are opposite
	// facts that a bare int cannot tell apart.
	trailingCompaction       bool
	trailingCompactionTokens int
}

// CompactedTo records a compaction's resulting transcript. The estimate must
// be the SAME number the in-memory re-baseline used (compact.go's
// SetLastTurn(estimateTokens(next))), or the gauge jumps at the moment a
// session resumes.
func (f *UsageFold) CompactedTo(msgs []provider.Message) {
	f.trailingCompaction = true
	f.trailingCompactionTokens = estimateTokens(msgs)
}

// CompactionSpend records what a compaction's own request cost.
func (f *UsageFold) CompactionSpend(u provider.Usage) {
	f.sinceLastTurn = f.sinceLastTurn.Add(u)
}

// AddUsage records one usage row. sideways marks a row that is not a TURN of
// this session.
//
// A sub-agent's spend is real but is not a turn, so it takes the compaction
// path: folded into the total, never made the baseline for lastTurn. Left on
// the usage path, a session whose final row was a child's would resume with
// the CHILD's prompt size as its context gauge — and a child is routinely
// larger than its parent, so the first threshold check would auto-compact a
// transcript that never grew.
//
// A host's side-channel call takes the same path. In memory it was never the
// snapshot (RecordSideChannelUsage books total-only), but on disk it was an
// ordinary row, so a session whose last row was a side chat's bespoke prompt
// resumed with THAT as its gauge.
func (f *UsageFold) AddUsage(u, cum provider.Usage, sideways bool) {
	if sideways {
		f.sinceLastTurn = f.sinceLastTurn.Add(u)
		return
	}
	if f.haveCum {
		f.prevCum = f.cumulative
	}
	f.betweenLastTwo = f.sinceLastTurn
	f.sinceLastTurn = provider.Usage{}
	// A real turn ran after that compaction, so its provider-reported prompt
	// size is the truth again and the estimate is superseded in its turn. Same
	// handoff as in memory, where the next completed request overwrites the
	// estimate SetLastTurn seeded.
	f.trailingCompaction = false
	f.trailingCompactionTokens = 0
	f.cumulative = cum
	f.haveCum = true
}

// Result returns the cumulative usage, the final turn's usage, and the resume
// baseline.
//
// lastTurn and resumeContext are separate because they answer different
// questions and only usually agree. lastTurn is what the final turn SPENT —
// history, and a compaction that ran afterwards has no business rewriting it.
// resumeContext is what the NEXT prompt will roughly cost, which is a claim
// about the transcript as it stands. They diverge in exactly one case: a
// compaction after the newest turn, where lastTurn describes a transcript that
// no longer exists.
func (f UsageFold) Result() (cumulative, lastTurn, resumeContext provider.Usage) {
	cumulative = f.cumulative
	if f.haveCum {
		// Charge the compactions that ran between the final two turns to the
		// baseline, not to the turn: delta(cum_N, cum_{N-1} + between) = u_N.
		prevCum := f.prevCum.Add(f.betweenLastTwo)
		// input/output are monotonic totals -> per-turn = delta.
		lastTurn.InputTokens = nonNegDelta(cumulative.InputTokens, prevCum.InputTokens)
		lastTurn.OutputTokens = nonNegDelta(cumulative.OutputTokens, prevCum.OutputTokens)
		// cache_read/write on the final row already represent the last prompt's
		// cache hit/creation, not a running total of bytes; use directly.
		lastTurn.CacheReadTokens = cumulative.CacheReadTokens - prevCum.CacheReadTokens
		if lastTurn.CacheReadTokens < 0 {
			lastTurn.CacheReadTokens = cumulative.CacheReadTokens
		}
		lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens - prevCum.CacheWriteTokens
		if lastTurn.CacheWriteTokens < 0 {
			lastTurn.CacheWriteTokens = cumulative.CacheWriteTokens
		}
		lastTurn.CostUSD = cumulative.CostUSD - prevCum.CostUSD
		if lastTurn.CostUSD < 0 {
			lastTurn.CostUSD = 0
		}
	}
	// A compaction after the newest turn never reached a cumulative row. Fold
	// it into the total — but NOT into lastTurn, which reports what that turn
	// actually spent and is not the compaction's to rewrite.
	cumulative = cumulative.Add(f.sinceLastTurn)

	// resumeContext is what a resuming host should SEED the gauge with, and it
	// is lastTurn only while lastTurn still describes the transcript.
	//
	// A compaction after the newest turn breaks that. Compacting and quitting
	// for the day leaves the gauge reporting the prompt size of a transcript
	// that no longer exists — measured on a real session, 98k against a ~5.8k
	// checkpoint, 17× high — and the first threshold check on resume then fires
	// a pointless auto-compact on an already-condensed transcript. That is the
	// same stale-high failure the corrections above defend against, arriving
	// through the one door they left open.
	//
	// Compaction spend is still never the answer: its input is transcript-sized
	// by construction, so seeding FROM the compaction's own usage would read
	// even higher. The answer is the compaction's RESULT — exactly what
	// compact.go does in memory, and this is that same re-baseline recovered
	// from the stored rows so a resumed session does not disagree with the one
	// that wrote it.
	resumeContext = lastTurn
	if f.trailingCompaction {
		resumeContext = provider.Usage{InputTokens: f.trailingCompactionTokens}
	}
	return cumulative, lastTurn, resumeContext
}

func nonNegDelta(cur, prev int) int {
	if cur < prev {
		return cur
	}
	return cur - prev
}
