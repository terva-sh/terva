package modes

import (
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The per-frame session metadata (transcript path, context window,
// subscription flag) is captured from every full SessionInfo the wire
// delivers — snapshots, session_updated, a switch's resume. Unlike the
// meter seed it is NOT bind-armed: a model switch that happened while this
// client was disconnected must land with the resubscribe's snapshot.
func TestNoteSessionMetaCapturesAndRefreshes(t *testing.T) {
	i := &Interactive{}
	i.noteSessionMeta(ctrlproto.SessionInfo{
		Path: "/home/u/.terva/sessions/abc.jsonl", ContextWindow: 200000, Subscription: true,
	})

	if got := i.CarrierSessionPath(); got != "/home/u/.terva/sessions/abc.jsonl" {
		t.Fatalf("CarrierSessionPath = %q", got)
	}
	i.mu.Lock()
	if i.carrierCtxWindow != 200000 || !i.carrierSubscription {
		t.Fatalf("ctxWindow=%d sub=%v, want 200000/true", i.carrierCtxWindow, i.carrierSubscription)
	}
	i.mu.Unlock()

	// A later info refreshes without any re-arm (a switch to a metered model).
	i.noteSessionMeta(ctrlproto.SessionInfo{
		Path: "/home/u/.terva/sessions/def.jsonl", ContextWindow: 128000, Subscription: false,
	})
	if got := i.CarrierSessionPath(); got != "/home/u/.terva/sessions/def.jsonl" {
		t.Fatalf("post-refresh CarrierSessionPath = %q", got)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.carrierCtxWindow != 128000 || i.carrierSubscription {
		t.Fatalf("post-refresh ctxWindow=%d sub=%v, want 128000/false", i.carrierCtxWindow, i.carrierSubscription)
	}
}

// ContextWindow 0 means the daemon doesn't know the model either — it is
// adopted (the render falls back to the local catalog on 0). An empty path
// never clobbers a known one.
func TestNoteSessionMetaZeroValues(t *testing.T) {
	i := &Interactive{}
	i.noteSessionMeta(ctrlproto.SessionInfo{Path: "/p/a.jsonl", ContextWindow: 200000})
	i.noteSessionMeta(ctrlproto.SessionInfo{ContextWindow: 0})

	if got := i.CarrierSessionPath(); got != "/p/a.jsonl" {
		t.Fatalf("empty path clobbered the cache: %q", got)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.carrierCtxWindow != 0 {
		t.Fatalf("carrierCtxWindow = %d, want 0 (daemon says unknown)", i.carrierCtxWindow)
	}
}

// The jailed badge mirrors the daemon's sandbox lock; every hello re-seeds
// it, so a daemon restarted with a different jail flag corrects the badge on
// reconnect.
func TestSetCarrierJailed(t *testing.T) {
	i := &Interactive{}
	i.SetCarrierJailed(true)
	i.mu.Lock()
	jailed := i.carrierJailed
	i.mu.Unlock()
	if !jailed {
		t.Fatal("carrierJailed not set")
	}
	i.SetCarrierJailed(false)
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.carrierJailed {
		t.Fatal("carrierJailed not cleared on re-seed")
	}
}

// The status bar's frame snapshot carries the wire-reported fields, and the
// ctx gauge prefers the daemon's window over the local catalog: a
// version-skewed attach client would otherwise gauge against the wrong (or
// zero) window.
func TestFrameSnapshotCarriesWireMeta(t *testing.T) {
	i := &Interactive{}
	i.noteSessionMeta(ctrlproto.SessionInfo{ContextWindow: 500000, Subscription: true})
	i.SetCarrierJailed(true)

	i.mu.Lock()
	snap := i.snapshotFrameLocked(turnRenderState{})
	i.mu.Unlock()
	if snap.carrierCtxWindow != 500000 || !snap.carrierSubscription || !snap.carrierJailed {
		t.Fatalf("snapshot meta = ctx %d sub %v jailed %v, want 500000/true/true",
			snap.carrierCtxWindow, snap.carrierSubscription, snap.carrierJailed)
	}
}

// A daemon that doesn't serve the tasks surface settles after one failed
// fill (empty non-nil view, not stale) instead of being re-fetched every
// render frame; the next change signal or dialog open retries.
func TestCarrierTasksFillFailureSettles(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.surfErr = errors.New("no such surface")
	i.cfg.Carrier = fc
	i.cfg.CarrierTasks = true

	i.fetchCarrierTasks() // the fill a stale render read would have kicked
	i.mu.Lock()
	stale, rows := i.carrierTasksStale, i.carrierTaskRows
	i.mu.Unlock()
	if stale || rows == nil || len(rows) != 0 {
		t.Fatalf("failed fill: stale=%v rows=%#v, want a settled empty view", stale, rows)
	}
	if n := i.swarmAgentCount(); n != 0 {
		t.Fatalf("swarmAgentCount = %d, want 0", n)
	}
}
