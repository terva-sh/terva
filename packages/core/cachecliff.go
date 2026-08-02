package core

import "terva.sh/terva/packages/provider"

// A provider-side cache outage is invisible from inside the session. The
// prefix ladder (prefixwatch.go) proves terva sent byte-stable, append-only
// requests; the provider bills them at full price anyway because routing never
// lands the request on a machine that holds the conversation's cache. Nothing
// in the transcript records it — the only witness is a run of usage rows whose
// cache read collapses to the floor every provider shares while the prompt
// keeps growing. One measured session re-read 14.4M prompt tokens across two
// such runs (~50 and ~31 consecutive dispatches) before anyone looked.
//
// This detector watches for exactly that signature and reports it while it is
// happening, when the user can still act on it (compact, switch model, stop).
// It deliberately reports only what it can defend:
//
//   - It arms only after a dispatch has proven the provider reports cache
//     reads at all. Endpoints that report no cache (local llama-style servers)
//     would otherwise look permanently collapsed.
//   - A collapse only counts when the PREVIOUS dispatch's prompt was big
//     enough that re-reading it is worth interrupting someone over.
//   - It fires after cliffFireAt consecutive collapses. OpenAI reasoning
//     models legitimately diverge once per user-message boundary (prior-turn
//     reasoning items are dropped at prompt assembly), so a single missed
//     dispatch is normal there and must stay below the threshold.
//   - Any dispatch whose prefix legitimately rebuilt (compaction, tool or
//     model change — anything the ladder reports as a non-append) resets the
//     run: that re-read is explained, and prefixwatch already records it.
type CacheCliff struct {
	// Dispatches is how many consecutive dispatches collapsed so far.
	Dispatches int
	// RereadTokens is the input the provider re-read across the run that the
	// previous dispatch's prompt already covered — the waste, not the bill.
	RereadTokens int
	// Ongoing is true while the run continues; the retract event (the run
	// ended: a dispatch hit again, or the prefix legitimately rebuilt) carries
	// false and zeroes.
	Ongoing bool
}

const (
	// cliffArmRead: a cache read at least this large must have been observed
	// before collapses count. Proves both that the provider reports caching
	// and that this conversation had a prefix worth caching.
	cliffArmRead = 16_384
	// cliffMinPrompt: the previous dispatch's prompt must be at least this
	// for its re-read to matter. Below it a full miss costs cents.
	cliffMinPrompt = 32_768
	// cliffFireAt is the consecutive-collapse count that fires. Two tolerates
	// the odd provider hiccup and the per-user-boundary miss reasoning
	// models bake in; three in a row on a large prompt is a pattern.
	cliffFireAt = 3
	// cliffBigPrompt drops the threshold to cliffFireAtBig: the boundary miss
	// the base threshold tolerates is exactly ONE dispatch, so a SECOND
	// consecutive collapse is already never legitimate — and on a prompt this
	// large, waiting for a third costs another dollar-class re-read (a
	// measured pair of 200K misses cost $2.10 and fired nothing).
	cliffBigPrompt = 100_000
	cliffFireAtBig = 2
)

// cacheCliffState is the detector's memory between dispatches. Guarded by
// Agent.mu; dispatches are sequential per agent, so this is belt-and-braces.
type cacheCliffState struct {
	// epochSeen is the ladder epoch (Agent.cliffEpoch) this state was built
	// against. A bump means the prefix legitimately rebuilt since the last
	// usage row, so the baseline below describes a prompt that no longer
	// exists.
	epochSeen int
	// armed flips once a real cache read ≥ cliffArmRead is seen; never back.
	armed bool
	// prevPrompt is the previous dispatch's full prompt (input + cache read +
	// cache write) — what the next dispatch's cache read is measured against.
	prevPrompt int
	streak     int
	reread     int
	// announced tracks whether observers currently believe a cliff is on, so
	// recovery fires exactly one retract and quiet sessions fire nothing.
	announced bool
}

// bumpCliffEpoch voids the detector's baseline. Called by watchPrefix for
// every dispatch whose prefix did not extend the previous one.
func (a *Agent) bumpCliffEpoch() {
	a.mu.Lock()
	a.cliffEpoch++
	a.mu.Unlock()
}

// observeDispatchCache feeds one dispatch's usage row to the detector. Called
// from the stream loop for the agent's own dispatches only — side-channel and
// delegated usage never describe this session's prompt, and folding them in
// is exactly the confusion that made a child's cold start read as the parent's
// cache collapsing.
func (a *Agent) observeDispatchCache(u provider.Usage) {
	if !a.prefixDivergenceRecordingOn() {
		// The detector leans on the ladder for "was this dispatch an append":
		// with recording off it cannot tell a collapse from a compaction.
		return
	}

	cached := u.CacheReadTokens + u.CacheWriteTokens
	prompt := u.InputTokens + cached

	a.mu.Lock()
	cs := &a.cliffState
	announce, retract := false, false
	switch {
	case cs.epochSeen != a.cliffEpoch:
		// The prefix legitimately rebuilt since the last row (compaction,
		// tool/model change, first dispatch). This row's miss is explained
		// and the old baseline is void.
		cs.epochSeen = a.cliffEpoch
		retract = cs.announced
		cs.streak, cs.reread, cs.announced = 0, 0, false
	case cs.armed && cs.prevPrompt >= cliffMinPrompt && cached < cs.prevPrompt/2:
		// Append-only dispatch, yet the provider served less than half the
		// prompt it had just seen: it re-read the rest at full price.
		grown := prompt - cs.prevPrompt
		if grown < 0 {
			grown = 0
		}
		if waste := u.InputTokens - grown; waste > 0 {
			cs.reread += waste
		}
		fireAt := cliffFireAt
		if cs.prevPrompt >= cliffBigPrompt {
			fireAt = cliffFireAtBig
		}
		cs.streak++
		if cs.streak >= fireAt {
			// Fire on every collapse past the threshold, not just the first:
			// the note tracks a changing fact, and each event carries the
			// run's current size.
			announce = true
			cs.announced = true
		}
	default:
		retract = cs.announced
		cs.streak, cs.reread, cs.announced = 0, 0, false
	}
	if u.CacheReadTokens >= cliffArmRead {
		cs.armed = true
	}
	cs.prevPrompt = prompt
	dispatches, reread := cs.streak, cs.reread
	a.mu.Unlock()

	if announce {
		a.fireCacheCliff(CacheCliff{Dispatches: dispatches, RereadTokens: reread, Ongoing: true})
	} else if retract {
		a.fireCacheCliff(CacheCliff{})
	}
}
