package swarm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// acquirerSpy is a fake Config.AcquireWorktree: it hands out a fixed
// directory and counts how many times the lease's Release hook runs.
// releases is incremented atomically so the -race detector and the
// at-most-once assertions stay honest even when several terminal paths
// race for the same agent.
type acquirerSpy struct {
	dir      string
	releases int32
	calls    int32
	err      error // when non-nil, AcquireWorktree fails the spawn
}

func (s *acquirerSpy) acquire(ctx context.Context, req WorktreeReq) (WorktreeLease, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return WorktreeLease{}, s.err
	}
	return WorktreeLease{
		Dir:     s.dir,
		Release: func() { atomic.AddInt32(&s.releases, 1) },
	}, nil
}

func (s *acquirerSpy) releaseCount() int { return int(atomic.LoadInt32(&s.releases)) }

// newWorktreeSwarm wires a Swarm whose AcquireWorktree is spy.acquire.
func newWorktreeSwarm(t *testing.T, spy *acquirerSpy, mk func(a *Agent) Runner) *Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	return New(Config{
		Root:            root,
		RepoRoot:        root,
		NewRunner:       mk,
		AcquireWorktree: spy.acquire,
	})
}

// TestWorktreeLeaseUsedAsDir asserts the leased directory becomes the
// agent's Dir (and therefore the child's --cwd) instead of RepoRoot.
func TestWorktreeLeaseUsedAsDir(t *testing.T) {
	leased := testsupport.TempDir(t)
	spy := &acquirerSpy{dir: leased}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, err := f.Spawn(context.Background(), "isolate me")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()

	if a.Dir != leased {
		t.Fatalf("Dir = %q; want leased dir %q", a.Dir, leased)
	}
	if a.Dir == f.cfg.RepoRoot {
		t.Fatal("Dir fell back to RepoRoot despite a non-empty lease")
	}
	if got := atomic.LoadInt32(&spy.calls); got != 1 {
		t.Fatalf("AcquireWorktree called %d times; want 1", got)
	}

	// The leased dir must flow through to the child's --cwd.
	args := defaultChildArgs("terva", a, "/tmp/sess.json", "/tmp/in.sock")
	if cwd := flagValue(args, "--cwd"); cwd != leased {
		t.Fatalf("--cwd = %q; want leased dir %q", cwd, leased)
	}
}

// TestWorktreeReleasedOnceOnCompletion: a normal completion releases
// the lease exactly once.
func TestWorktreeReleasedOnceOnCompletion(t *testing.T) {
	spy := &acquirerSpy{dir: testsupport.TempDir(t)}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, err := f.Spawn(context.Background(), "finish cleanly")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	// Give any belt-and-suspenders path a moment to (not) double-fire.
	time.Sleep(20 * time.Millisecond)
	if got := spy.releaseCount(); got != 1 {
		t.Fatalf("release count = %d; want exactly 1", got)
	}
}

// TestWorktreeReleasedOnceOnFailure: a runner error is still a terminal
// transition and must release exactly once.
func TestWorktreeReleasedOnceOnFailure(t *testing.T) {
	spy := &acquirerSpy{dir: testsupport.TempDir(t)}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return errors.New("boom") })
	})
	a, err := f.Spawn(context.Background(), "explode")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	time.Sleep(20 * time.Millisecond)
	if a.Status() != StatusFailed {
		t.Fatalf("status = %s; want failed", a.Status())
	}
	if got := spy.releaseCount(); got != 1 {
		t.Fatalf("release count = %d; want exactly 1", got)
	}
}

// TestWorktreeReleasedOnceOnStop: Stop on a running agent releases the
// lease exactly once across the Stop path and the runner's terminal
// finalisation.
func TestWorktreeReleasedOnceOnStop(t *testing.T) {
	started := make(chan struct{})
	spy := &acquirerSpy{dir: testsupport.TempDir(t)}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	a, err := f.Spawn(context.Background(), "long runner")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	// Let the Stop background goroutine reach its finish() backstop too.
	time.Sleep(50 * time.Millisecond)
	if a.Status() != StatusKilled {
		t.Fatalf("status = %s; want killed", a.Status())
	}
	if got := spy.releaseCount(); got != 1 {
		t.Fatalf("release count = %d; want exactly 1", got)
	}
}

// TestWorktreeReleasedOnceOnStopAll: StopAll over several leased agents
// releases each lease exactly once.
func TestWorktreeReleasedOnceOnStopAll(t *testing.T) {
	const n = 3
	spies := make([]*acquirerSpy, n)
	started := make(chan struct{}, n)

	// Each agent gets its own spy so we can assert per-lease counts.
	// Map the per-agent spy by the leased dir, which the factory reads
	// off the agent it's building.
	var idx int32
	byDir := map[string]*acquirerSpy{}
	for i := range spies {
		spies[i] = &acquirerSpy{dir: testsupport.TempDir(t)}
		byDir[spies[i].dir] = spies[i]
	}
	acquire := func(ctx context.Context, req WorktreeReq) (WorktreeLease, error) {
		i := atomic.AddInt32(&idx, 1) - 1
		s := spies[i]
		return WorktreeLease{
			Dir:     s.dir,
			Release: func() { atomic.AddInt32(&s.releases, 1) },
		}, nil
	}

	root := testsupport.TempDir(t)
	f := New(Config{
		Root:            root,
		RepoRoot:        root,
		AcquireWorktree: acquire,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})

	agents := make([]*Agent, n)
	for i := 0; i < n; i++ {
		a, err := f.Spawn(context.Background(), "agent")
		if err != nil {
			t.Fatal(err)
		}
		agents[i] = a
	}
	for i := 0; i < n; i++ {
		<-started
	}
	f.StopAll()
	for _, a := range agents {
		a.Wait()
	}
	time.Sleep(50 * time.Millisecond)
	for i, s := range spies {
		if got := s.releaseCount(); got != 1 {
			t.Fatalf("agent %d (dir %s): release count = %d; want exactly 1", i, s.dir, got)
		}
	}
	_ = byDir
}

// TestNilAcquirePreservesRepoRoot: with no AcquireWorktree hook (the
// default), every agent keeps RepoRoot as its Dir — today's behavior.
func TestNilAcquirePreservesRepoRoot(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
		},
	})
	a, err := f.Spawn(context.Background(), "no isolation")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if a.Dir != root {
		t.Fatalf("Dir = %q; want RepoRoot %q", a.Dir, root)
	}
}

// TestAcquireErrorFailsSpawn: an AcquireWorktree that errors fails the
// spawn and surfaces the underlying error (the hard-error-on-explicit-
// request contract). No agent is registered.
func TestAcquireErrorFailsSpawn(t *testing.T) {
	wantErr := errors.New("no extension registered for tool \"worktree_create\"")
	spy := &acquirerSpy{err: wantErr}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	a, err := f.Spawn(context.Background(), "doomed")
	if err == nil {
		t.Fatal("expected spawn to fail when AcquireWorktree errors")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error %v does not wrap the acquire error %v", err, wantErr)
	}
	if a != nil {
		t.Fatalf("expected nil agent on failed spawn; got %#v", a)
	}
	// Nothing should have been registered.
	if got := len(f.List()); got != 0 {
		t.Fatalf("swarm has %d agents after a failed spawn; want 0", got)
	}
	// The lease was never granted, so Release must never have fired.
	if got := spy.releaseCount(); got != 0 {
		t.Fatalf("release count = %d after failed acquire; want 0", got)
	}
}

// flagValue returns the value following flag in args, or "".
func flagValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
