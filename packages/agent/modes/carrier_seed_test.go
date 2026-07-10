package modes

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

func sessionInfo(cost float64, ctxTokens int) ctrlproto.SessionInfo {
	return ctrlproto.SessionInfo{
		Usage:         core.WireUsage{Input: 10, Output: 5, CostUSD: cost},
		ContextTokens: ctxTokens,
	}
}

// The cost and context gauges are seeded from the binding's first snapshot.
// They used to be read off the crutch agent (Cost(), LastTurnUsage()) at
// construction and on session load; SessionInfo has carried both all along,
// and the TUI discarded it. Per-turn updates already ride the usage events.
func TestSeedSessionMetersHydratesFromFirstSnapshot(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()
	i.seedSessionMeters(sessionInfo(1.25, 4000))

	i.mu.Lock()
	defer i.mu.Unlock()
	if got := i.cumUsage.CostUSD; got != 1.25 {
		t.Fatalf("cumUsage.CostUSD = %v, want 1.25", got)
	}
	if i.cumUsage.InputTokens != 10 {
		t.Fatalf("cumUsage.InputTokens = %d, want 10", i.cumUsage.InputTokens)
	}
	if i.lastCtxInput != 4000 {
		t.Fatalf("lastCtxInput = %d, want 4000", i.lastCtxInput)
	}
	// The bound session's history is the burn-rate base, not live spend.
	if i.costBase != 1.25 {
		t.Fatalf("costBase = %v, want 1.25", i.costBase)
	}
	if i.costBaseAt.IsZero() {
		t.Fatal("costBaseAt not anchored")
	}
}

// Snapshots also ride compaction, clear, and every reconnect's resubscribe.
// Re-seeding there would re-anchor the $/hr epoch mid-session and wipe the
// spend the status bar has been accumulating from usage events.
func TestSeedSessionMetersIgnoresLaterSnapshots(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()
	i.seedSessionMeters(sessionInfo(1.25, 4000))

	// The turn loop has since accrued spend off the wire.
	i.mu.Lock()
	i.cumUsage.CostUSD = 3.50
	i.mu.Unlock()

	i.seedSessionMeters(sessionInfo(1.25, 4000)) // a compact rebroadcast

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cumUsage.CostUSD != 3.50 {
		t.Fatalf("a later snapshot rolled cumUsage back to %v", i.cumUsage.CostUSD)
	}
	if i.costBase != 1.25 {
		t.Fatalf("a later snapshot re-anchored costBase to %v", i.costBase)
	}
}

// Only a fresh binding re-arms the seed.
func TestSeedSessionMetersRearmsOnBind(t *testing.T) {
	i := &Interactive{}
	i.armCarrierBind()
	i.seedSessionMeters(sessionInfo(1.25, 4000))

	i.armCarrierBind()
	i.seedSessionMeters(sessionInfo(9.00, 120))

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cumUsage.CostUSD != 9.00 || i.lastCtxInput != 120 || i.costBase != 9.00 {
		t.Fatalf("re-bind did not re-seed: cost=%v ctx=%d base=%v",
			i.cumUsage.CostUSD, i.lastCtxInput, i.costBase)
	}
}
