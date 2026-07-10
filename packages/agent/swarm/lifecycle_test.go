package swarm

import (
	"context"
	"testing"
	"time"
)

// TestSpawnSurvivesCallerContextCancel: an agent's lifetime is
// swarm-scoped, not call-scoped. Spawns arrive on a turn's tool-dispatch
// context, and the turn ending — Esc-cancel, a provider error, or plain
// completion — must not tear down the background worker. (Regression:
// agents once died to exec.CommandContext through exactly this chain —
// four reviewers were killed mid-report when the host's context
// collapsed.)
func TestSpawnSurvivesCallerContextCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	ctxDied := make(chan struct{}, 1)
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				ctxDied <- struct{}{}
				return ctx.Err()
			case <-release:
				return nil
			}
		})
	})

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	a, err := f.Spawn(callerCtx, "outlive the spawning turn")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	// The spawning turn ends: its context is cancelled.
	cancelCaller()

	select {
	case <-ctxDied:
		t.Fatal("agent context died with the caller's context — sub-agent tethered to the turn")
	case <-time.After(100 * time.Millisecond):
	}
	if got := a.Status(); got != StatusRunning {
		t.Fatalf("status after caller cancel = %s; want running", got)
	}

	close(release)
	a.Wait()
	if got := a.Status(); got != StatusDone {
		t.Fatalf("final status = %s; want done", got)
	}
}

// TestStopAllAndWaitDrainsAndFinalizes: the shutdown path must not
// return until every child reached a terminal state (or the bound
// expired), so a host about to exit doesn't yank the stdout pipes out
// from under children mid-write.
func TestStopAllAndWaitDrainsAndFinalizes(t *testing.T) {
	started := make(chan struct{}, 2)
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			started <- struct{}{}
			<-ctx.Done() // a busy child: only the stop cancel ends it
			return ctx.Err()
		})
	})
	f.cfg.StopGrace = 50 * time.Millisecond // no inbox listener → immediate cancel path anyway

	a1, err := f.Spawn(context.Background(), "task one")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := f.Spawn(context.Background(), "task two")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("runner did not start")
		}
	}

	doneWaiting := make(chan struct{})
	go func() {
		f.StopAllAndWait(2 * time.Second)
		close(doneWaiting)
	}()
	select {
	case <-doneWaiting:
	case <-time.After(3 * time.Second):
		t.Fatal("StopAllAndWait did not return")
	}
	for _, a := range []*Agent{a1, a2} {
		select {
		case <-a.done:
		default:
			t.Fatalf("agent %s not finalised after StopAllAndWait", a.ID)
		}
		if got := a.Status(); got != StatusKilled {
			t.Fatalf("agent %s status = %s; want killed", a.ID, got)
		}
	}
}

// TestStopAllAndWaitNoAgentsReturnsImmediately guards the empty case —
// an idle host's shutdown must not sit out the full bound.
func TestStopAllAndWaitNoAgentsReturnsImmediately(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	start := time.Now()
	f.StopAllAndWait(5 * time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("StopAllAndWait with no agents took %v", elapsed)
	}
}
