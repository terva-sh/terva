package modes

import (
	"context"
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// newInteractiveForWatchTest builds the minimal Interactive scaffolding
// the auto-swarm watcher needs. trackSwarmAgent only touches
// swarmWatchMu/swarmWatch, the status mutex (via flushSwarmSummary ->
// SubmitOrQueue, which no-ops with a nil agent), and the dirty channel
// (via invalidate). We hand-build those instead of standing up the
// whole TUI. The swarm runner crashes promptly so the agent reaches a
// terminal state WITHOUT ever emitting a task-level turn_end — exactly
// the case that used to wedge the watcher forever.
func newInteractiveForWatchTest(t *testing.T, run swarm.RunnerFunc) (*Interactive, *swarm.Swarm) {
	t.Helper()
	root := testsupport.TempDir(t)
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return run
		},
	})
	iv := &Interactive{dirty: make(chan struct{}, 1), turns: newTurnEngine()}
	return iv, f
}

// TestSwarmWatcherFinalisesOnTerminalCrash pins the fix for the wedge:
// a sub-agent that crashes (or is stopped) before emitting a task-level
// turn_end must still complete its watcher entry via the terminal-state
// fallback, so the summary flushes and swarmWatch drains instead of
// staying blocked forever.
func TestSwarmWatcherFinalisesOnTerminalCrash(t *testing.T) {
	// Runner returns an error immediately and never calls any turn_end,
	// so OnTurnEnd never fires for this agent.
	iv, f := newInteractiveForWatchTest(t, func(ctx context.Context, sink swarm.Sink) error {
		return errors.New("boom")
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(), "crash task")
	if err != nil {
		t.Fatal(err)
	}

	iv.trackSwarmAgent(a, "crash task")

	// The terminal-state waiter should mark the entry done and flush,
	// clearing swarmWatch. Without the fallback this never happens.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		iv.swarmWatchMu.Lock()
		n := len(iv.swarmWatch)
		iv.swarmWatchMu.Unlock()
		if n == 0 {
			return // flushed: entry finalised via terminal state
		}
		time.Sleep(5 * time.Millisecond)
	}
	iv.swarmWatchMu.Lock()
	n := len(iv.swarmWatch)
	done := n == 1 && iv.swarmWatch[0].done
	iv.swarmWatchMu.Unlock()
	t.Fatalf("watcher entry never finalised after crash: swarmWatch len=%d done=%v", n, done)
}

// TestSwarmWatcherTurnEndPathFinalises exercises the happy path the
// runner drives via OnTurnEnd, by invoking the shared finaliser
// directly (the runner-internal turn_end machinery isn't reachable
// from a test without the daemon protocol). A long-lived agent that
// never reaches a terminal state must still flush once its task-level
// turn_end arrives, and the entry must drain from swarmWatch.
func TestSwarmWatcherTurnEndPathFinalises(t *testing.T) {
	// Long-lived runner: blocks until cancelled, mimicking a daemon that
	// stays up after its initial task. Wait() therefore does NOT unblock
	// during the test, so finalisation here can only come from the
	// turn_end path.
	iv, f := newInteractiveForWatchTest(t, func(ctx context.Context, sink swarm.Sink) error {
		<-ctx.Done()
		return ctx.Err()
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(), "long task")
	if err != nil {
		t.Fatal(err)
	}
	iv.trackSwarmAgent(a, "long task")

	iv.swarmWatchMu.Lock()
	if len(iv.swarmWatch) != 1 {
		iv.swarmWatchMu.Unlock()
		t.Fatalf("expected 1 tracked entry, got %d", len(iv.swarmWatch))
	}
	entry := iv.swarmWatch[0]
	iv.swarmWatchMu.Unlock()

	// Drive the same finaliser the runner's OnTurnEnd callback calls.
	iv.finalizeSwarmEntry(entry, "")

	iv.swarmWatchMu.Lock()
	n := len(iv.swarmWatch)
	iv.swarmWatchMu.Unlock()
	if n != 0 {
		t.Fatalf("turn_end finalise did not flush: swarmWatch len=%d", n)
	}

	// A subsequent terminal-state finalise for the same entry (e.g. the
	// Wait() fallback firing later) must be an idempotent no-op.
	iv.finalizeSwarmEntry(entry, "late")
	if entry.err != "" {
		t.Errorf("idempotent finalise overwrote err: got %q, want unchanged empty", entry.err)
	}
}
