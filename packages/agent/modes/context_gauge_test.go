package modes

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// The status bar's context gauge after the transcript is replaced.
//
// Compaction shrinks a transcript; it does not empty one. The TUI used to set
// the gauge to 0 on compact_end and again in the /compact completion closure,
// so a conversation that had just been condensed to (say) 30k tokens reported
// an empty context until the next turn happened to land a usage event. The
// daemon knew the real figure the whole time -- compact.go re-baselines
// LastTurnUsage on the post-compaction estimate, and every snapshot carries it
// on SessionInfo.ContextTokens.

func gaugeInteractive(t *testing.T) *Interactive {
	t.Helper()
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()
	return i
}

func gaugeValue(i *Interactive) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastCtxInput
}

// A snapshot is the daemon saying "the transcript was replaced under you" --
// the one signal common to compact, auto-compact, clear, and a session switch.
// The figure it carries must be adopted every time, not only on the first one:
// seedSessionMeters is bind-armed and goes inert after the binding is seeded.
func TestASnapshotReSeedsTheContextGauge(t *testing.T) {
	i := gaugeInteractive(t)

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session: ctrlproto.SessionInfo{ContextTokens: 120_000},
	}))
	if got := gaugeValue(i); got != 120_000 {
		t.Fatalf("gauge = %d after the first snapshot, want 120000", got)
	}

	// The compaction lands: a second snapshot, a much smaller context.
	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session: ctrlproto.SessionInfo{ContextTokens: 31_500},
	}))
	if got := gaugeValue(i); got != 31_500 {
		t.Errorf("gauge = %d after the post-compaction snapshot, want 31500 -- "+
			"the daemon's figure was on the wire and the client ignored it", got)
	}
}

// Zero is a real answer, not a sentinel: it is what an emptied conversation
// reports. A "only adopt when non-zero" guard would make /clear the one case
// the gauge could never follow, which is the bug in the other direction.
func TestAnEmptiedConversationReportsAnEmptyGauge(t *testing.T) {
	i := gaugeInteractive(t)

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session: ctrlproto.SessionInfo{ContextTokens: 90_000},
	}))
	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session: ctrlproto.SessionInfo{ContextTokens: 0},
	}))
	if got := gaugeValue(i); got != 0 {
		t.Errorf("gauge = %d after a clear, want 0", got)
	}
}

// compact_end announces the condense. It carries the summarization's own COST
// (deliberately -- see EvCompactEnd), never a context sample, so it is not the
// event that can correct the gauge. What it must not do is destroy it: the
// snapshot alongside carries the truth, and a mid-turn auto-compact that
// broadcasts no snapshot is corrected by the very next usage event. Zeroing
// beat both to it with a number that was never true.
func TestCompactEndDoesNotZeroTheGauge(t *testing.T) {
	i := gaugeInteractive(t)
	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session: ctrlproto.SessionInfo{ContextTokens: 47_000},
	}))

	// handleCarrierEvent, NOT handleWireEvent: compact_end is handled in the
	// carrier switch, and the wire switch has no case for it at all. Driven
	// through the wrong one this test passed with the zeroing restored -- a
	// vacuous guard, caught by ablating the fix and watching it stay green.
	i.handleCarrierEvent(conv(core.WireEvent{
		Type:  "compact_end",
		Usage: &core.WireUsage{Input: 118_000, Output: 900},
	}))

	if got := gaugeValue(i); got == 0 {
		t.Fatal("compact_end zeroed the context gauge: the conversation still has a context, and the daemon knows its size")
	}
	if got := gaugeValue(i); got != 47_000 {
		t.Errorf("gauge = %d, want the standing 47000 -- compact_end must not move it at all, "+
			"least of all to the summarizer's own 118000-token read", got)
	}
}
