package core

import (
	"sync"

	"terva.sh/terva/packages/provider"
)

// CostTracker accumulates usage across turns in a session.
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
type CostTracker struct {
	mu       sync.Mutex
	Total    provider.Usage
	LastTurn provider.Usage
}

// Add folds u into the running total, records u as the last-turn
// snapshot, and returns the new cumulative value.
func (c *CostTracker) Add(u provider.Usage) provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
	c.LastTurn = u
	return c.Total
}

// AddTotalOnly folds u into the running total WITHOUT touching the
// last-turn snapshot. Compaction's summarization request uses it: the
// spend is real, but the snapshot is the context gauge — letting a
// transcript-sized summarization request overwrite the freshly
// re-baselined value would leave every threshold check reading
// stale-high again.
func (c *CostTracker) AddTotalOnly(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = c.Total.Add(u)
}

// CumulativeTotal returns the cumulative usage under the tracker lock.
func (c *CostTracker) CumulativeTotal() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Total
}

// LastTurnUsage returns the most recent per-turn snapshot under the
// tracker lock.
func (c *CostTracker) LastTurnUsage() provider.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastTurn
}

// SetTotal overwrites the cumulative usage. Used to seed a baseline
// when transferring state from another agent.
func (c *CostTracker) SetTotal(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Total = u
}

// SetLastTurn overwrites the per-turn snapshot. Used on resume so the
// gauge reflects the last persisted turn instead of zero.
func (c *CostTracker) SetLastTurn(u provider.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastTurn = u
}
