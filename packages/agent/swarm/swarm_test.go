package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// newTestSwarm builds a Swarm rooted in t.TempDir with the in-memory
// and a Runner factory the test controls. Returns the swarm plus a
// slice of runners keyed by spawn order so tests can assert they
// were actually invoked.
func newTestSwarm(t *testing.T, mk func(a *Agent) Runner) *Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	return New(Config{
		Root:      root,
		RepoRoot:  root,
		NewRunner: mk,
	})
}

func TestSpawnRunsAndCompletes(t *testing.T) {
	ran := make(chan string, 1)
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			ran <- a.Task
			sink.Activity("hello")
			sink.Transcript("line one")
			sink.Transcript("line two")
			return nil
		})
	})
	a, err := f.Spawn(context.Background(), "do a thing")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ran:
		if got != "do a thing" {
			t.Fatalf("runner got task %q; want %q", got, "do a thing")
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	a.Wait()
	if a.Status() != StatusDone {
		t.Fatalf("status %s; want done", a.Status())
	}
	if got := a.Transcript(); len(got) != 2 || got[0] != "line one" || got[1] != "line two" {
		t.Fatalf("transcript = %q", got)
	}
	if !strings.Contains(a.ID, "do-a-thing") {
		t.Fatalf("id %q missing slug", a.ID)
	}
	// Every agent shares the host's RepoRoot.
	if a.Dir != f.cfg.RepoRoot {
		t.Fatalf("dir = %q; want repo root %q", a.Dir, f.cfg.RepoRoot)
	}
}

// TestSpawnAgentSharesRepoRoot verifies the only-mode-we-support:
// every spawned agent points its cwd at the parent terva's RepoRoot.
func TestSpawnAgentSharesRepoRoot(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, err := f.Spawn(context.Background(), "share me")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if a.Dir != f.cfg.RepoRoot {
		t.Fatalf("Dir = %q; want RepoRoot %q", a.Dir, f.cfg.RepoRoot)
	}
}

func TestSpawnEmptyTaskFails(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	if _, err := f.Spawn(context.Background(), "   "); err == nil {
		t.Fatal("expected error on empty task")
	}
}

func TestRunnerErrorMarksFailed(t *testing.T) {
	wantErr := errors.New("boom")
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return wantErr })
	})
	a, _ := f.Spawn(context.Background(), "explode")
	a.Wait()
	if a.Status() != StatusFailed {
		t.Fatalf("status %s; want failed", a.Status())
	}
	if !errors.Is(a.Err(), wantErr) {
		t.Fatalf("err = %v", a.Err())
	}
	if !strings.Contains(a.Activity(), "boom") {
		t.Fatalf("activity = %q; want it to mention the error", a.Activity())
	}
}

func TestStopCancelsRunningAgent(t *testing.T) {
	started := make(chan struct{})
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	a, _ := f.Spawn(context.Background(), "long")
	<-started
	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if a.Status() != StatusKilled {
		t.Fatalf("status = %s; want killed", a.Status())
	}
}

func TestStopAfterDoneIsNoop(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, _ := f.Spawn(context.Background(), "quick")
	a.Wait()
	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("stop after done: %v", err)
	}
	if a.Status() != StatusDone {
		t.Fatalf("status flipped to %s", a.Status())
	}
}

func TestGetPrefixMatch(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, _ := f.Spawn(context.Background(), "alpha task")
	a.Wait()
	// Full id works.
	if got := f.Get(a.ID); got != a {
		t.Fatal("get by full id failed")
	}
	// Slug prefix works as long as it's unique.
	if got := f.Get("alpha"); got != a {
		t.Fatal("get by prefix failed")
	}
	// Bogus id returns nil.
	if got := f.Get("zzz-nope"); got != nil {
		t.Fatalf("expected nil; got %#v", got)
	}
}

func TestRemoveRequiresTerminalState(t *testing.T) {
	hold := make(chan struct{})
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			<-hold
			return nil
		})
	})
	a, _ := f.Spawn(context.Background(), "still going")
	// Wait for run goroutine to flip to running.
	for i := 0; i < 100 && a.Status() != StatusRunning; i++ {
		time.Sleep(time.Millisecond)
	}
	if err := f.Remove(a.ID); err == nil {
		t.Fatal("remove of running agent should fail")
	}
	close(hold)
	a.Wait()
	if err := f.Remove(a.ID); err != nil {
		t.Fatalf("remove after done: %v", err)
	}
	if got := f.Get(a.ID); got != nil {
		t.Fatal("agent still present after remove")
	}
}

func TestSnapshotIsStableAcrossAccess(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sink.Transcript("chunk")
				sink.Activity("step")
			}
			return nil
		})
	})
	a, _ := f.Spawn(context.Background(), "race")
	// Hammer Snapshot while the runner is writing; the -race detector
	// is the real assertion here.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = a.Snapshot()
			}
		}
	}()
	wg.Wait()
	a.Wait()
	close(stop)
	if a.Status() != StatusDone {
		t.Fatalf("status = %s", a.Status())
	}
}

func TestTaskSlug(t *testing.T) {
	cases := map[string]string{
		"fix the login form":                   "fix-the-login-form",
		"  weird --- spaces!!  ":               "weird-spaces",
		"":                                     "agent",
		"a-very-long-task-name-that-overflows": "a-very-long-task-name-th",
	}
	for in, want := range cases {
		if got := taskSlug(in); got != want {
			t.Errorf("taskSlug(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestSnapshotAllSorted(t *testing.T) {
	f := newTestSwarm(t, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a1, _ := f.Spawn(context.Background(), "first")
	// Force second spawn into a later nanosecond bucket.
	time.Sleep(2 * time.Millisecond)
	a2, _ := f.Spawn(context.Background(), "second")
	a1.Wait()
	a2.Wait()
	snaps := f.SnapshotAll()
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots; got %d", len(snaps))
	}
	if !snaps[0].Started.Before(snaps[1].Started) && !snaps[0].Started.Equal(snaps[1].Started) {
		t.Fatal("snapshots not in spawn order")
	}
}
