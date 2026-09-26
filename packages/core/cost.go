package core

import (
	"sync"

	"terva.sh/terva/packages/provider"
)

// costTracker accumulates usage across turns in a session.
//
// Total is the cumulative usage shown in the status bar's "$x.xx"
// readout. LastTurn is the per-turn usage of the most recent
// completed turn; the TUI uses LastTurn.InputTokens+cache as a
// proxy for "current context size" so the X%/Ymax gauge tracks the
// prompt size that just went to the model.
//
// mu guards both fields so the agent loop can fold usage in from the
// stream goroutine while hosts read the totals concurrently (status
// bar, Telegram bot, SDK). It is the tracker's own lock rather than
// the Agent mutex so Add can run inside oneTurn's stream loop without
// serializing against transcript operations.
type costTracker struct {
	mu       sync.Mutex
	Total    provider.Usage
	LastTurn provider.Usage
	// Delegated is the part of Total spent by sub-agents on this session's
	// behalf, rather than by its own turns. A subset of Total, never added
	// alongside it.
	Delegated provider.Usage
	// SideChannel is the part of Total spent by the host's one-off completions
	// on this session's credentials (an idle next-step suggestion, a side
	// chat, the Stage router) rather than by its own turns. A subset of Total,
	// like Delegated, and kept apart for the same reason: a headline that
	// merges them cannot say what the session's own turns cost, and a
	// side-channel call is spend the user did not type a prompt for.
	SideChannel provider.Usage

	// recent is the tail of per-response usage records, oldest first, capped
	// at recentCap. Totals answer "what has this session spent"; this answers
	// "what just changed", which is the only form a cache reading is useful
	// in — a hit rate averaged over a session hides the turn where the prefix
	// broke, and that turn is the entire diagnosis.
	//
	// Deliberately not persisted and deliberately not seeded on resume. The
	// strip is a live instrument: it starts empty and fills as requests land,
	// while the session's cumulative figures (which DO survive resume, via
	// SetTotal) carry the history. Reconstructing a per-response series from
	// the session file means deriving deltas across cumulative rows with the
	// compaction corrections session.SessionUsageDetail already carries, for a strip
	// that is stale the moment the next request lands.
	recent []provider.Usage
}

// recentCap bounds the per-response ring. Wide enough that a strip covers the
// working span of a conversation — the last few turns and their tool steps —
// and small enough that the whole thing rides a status payload without anyone
// thinking about it. Each record is five ints and two floats.
const recentCap = 32

// Add folds u into the running total, records u as the last-turn
// snapshot, and returns the new cumulative value.
func (c *costTracker) Add(u provider.Usage) provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
	c.LastTurn = u
	c.recent = append(c.recent, u)
	if len(c.recent) > recentCap {
		// Copy rather than reslice: dropping the head off a shared backing
		// array keeps the evicted records alive for as long as the session
		// runs, and RecentUsage hands out a copy of exactly this slice.
		c.recent = append([]provider.Usage(nil), c.recent[len(c.recent)-recentCap:]...)
	}
	return c.Total
}

// RecentUsage returns the per-response tail, oldest first.
//
// Only Add appends: compaction (AddTotalOnly) and delegation (AddDelegated)
// are spend, not prompts this session sent, and folding them in would put a
// transcript-sized cold read in the middle of the strip labelled as a cache
// miss. That is the same distinction those two methods already draw for
// LastTurn, for the same reason.
func (c *costTracker) RecentUsage() []provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Usage(nil), c.recent...)
}

// AddTotalOnly folds u into the running total WITHOUT touching the
// last-turn snapshot. Compaction's summarization request uses it: the
// spend is real, but the snapshot is the context gauge — letting a
// transcript-sized summarization request overwrite the freshly
// re-baselined value would leave every threshold check reading
// stale-high again.
func (c *costTracker) AddTotalOnly(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
}

// AddDelegated folds u into the running total AND into a second, delegated
// tally. The total is what the session spent; the delegated tally is how much
// of it a sub-agent spent on the session's behalf.
//
// Both, not either. A merged figure hides which is which, and a separate one
// leaves the headline understating what the session caused — a coordinator
// could spend an order of magnitude more than its record shows and truthfully
// report the small number. Delegation is the one action whose cost is unbounded
// by the coordinator's own turn, so it is the one that most needs saying.
//
// Total-only for the same reason as AddTotalOnly: a sub-agent's prompt is not
// this session's context, and letting it overwrite the per-turn snapshot would
// leave every compaction threshold reading a size the transcript never had.
func (c *costTracker) AddDelegated(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
	c.Delegated = c.Delegated.Add(u)
}

// DelegatedTotal returns the delegated tally under the tracker lock.
func (c *costTracker) DelegatedTotal() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Delegated
}

// AddSideChannel folds u into the running total AND into the side-channel
// tally, and touches nothing else. Total-only as far as the context gauge is
// concerned, for the reason AddTotalOnly gives: a side-channel request's
// prompt is not this session's context, and letting one overwrite LastTurn
// would leave every threshold check reading a size the transcript never had.
func (c *costTracker) AddSideChannel(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
	c.SideChannel = c.SideChannel.Add(u)
}

// SideChannelTotal returns the side-channel tally under the tracker lock.
func (c *costTracker) SideChannelTotal() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.SideChannel
}

// CumulativeTotal returns the cumulative usage under the tracker lock.
func (c *costTracker) CumulativeTotal() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Total
}

// LastTurnUsage returns the most recent per-turn snapshot under the
// tracker lock.
func (c *costTracker) LastTurnUsage() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastTurn
}

// SetTotal overwrites the cumulative usage. Used to seed a baseline
// when transferring state from another agent.
func (c *costTracker) SetTotal(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = u
}

// SetLastTurn overwrites the per-turn snapshot. Used on resume so the
// gauge reflects the last persisted turn instead of zero.
func (c *costTracker) SetLastTurn(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastTurn = u
}
