package swarm

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// blockedSwarm spawns agents that run until the test closes the returned
// channel (or the swarm is stopped), so their in-flight state is observable
// for as long as the test needs it — and so a test that wants a FINISHED
// agent gets one by letting the runner return, which is how an agent reaches
// a terminal status in production.
func blockedSwarm(t *testing.T) (*Swarm, chan struct{}) {
	t.Helper()
	root := testsupport.TempDir(t)
	release := make(chan struct{})
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error {
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		},
	})
	t.Cleanup(f.StopAll)
	return f, release
}

// Finding C3: two spawns returned immediately and $15.63 — 39% of the session —
// surfaced only in the recap seven minutes later. The money was never
// unmeasured; a child's cumulative usage updates live on every usage event
// (IngestEvent -> setUsage). Nothing asked for it until the child finished.
func TestInFlightSpendSumsRunningAgents(t *testing.T) {
	f, _ := blockedSwarm(t)
	a1, err := f.Spawn(context.Background(), "task one")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := f.Spawn(context.Background(), "task two")
	if err != nil {
		t.Fatal(err)
	}

	// Nothing reported yet: running agents, no usage.
	if u, n := f.InFlightSpend(); n != 2 || u != (provider.Usage{}) {
		t.Fatalf("before any usage: got %d agents / %+v, want 2 / zero", n, u)
	}

	a1.setUsage(provider.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 4.50})
	a2.setUsage(provider.Usage{InputTokens: 200, OutputTokens: 20, CostUSD: 11.13})

	u, n := f.InFlightSpend()
	if n != 2 {
		t.Errorf("agents = %d, want 2", n)
	}
	if u.CostUSD != 15.63 {
		t.Errorf("cost = %v, want 15.63", u.CostUSD)
	}
	if u.InputTokens != 300 || u.OutputTokens != 30 {
		t.Errorf("tokens = %d in / %d out, want 300/30", u.InputTokens, u.OutputTokens)
	}
}

// The boundary that keeps this from double-counting: a FINISHED child's spend is
// booked against the parent by the recap (RecordDelegatedUsage), so counting it
// here too would show it twice to anyone reading both numbers.
// The agent finishes by letting its runner RETURN, not by having the test
// write StatusDone behind the runner's back. That write raced the first thing
// f.run does — a.setStatus(StatusRunning) — which lands after it whenever the
// goroutine has not been scheduled yet, putting the agent back in flight and
// failing this test about 1 run in 20. Waiting for StatusRunning before
// overriding it would close that window; returning from the runner removes it,
// and gets a terminal status the way production does, through run's own switch.
//
// The agent stays in the swarm once done — Archive is explicit — so this is
// still the exclusion being asserted and not an empty list.
func TestInFlightSpendExcludesFinishedAgents(t *testing.T) {
	f, release := blockedSwarm(t)
	a, err := f.Spawn(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	a.setUsage(provider.Usage{CostUSD: 9.99})
	if _, n := f.InFlightSpend(); n != 1 {
		t.Fatalf("precondition: agent should be in flight, got %d", n)
	}

	close(release)
	a.Wait()
	if got := a.Snapshot().Status; got != StatusDone {
		t.Fatalf("agent finished as %q, want %q", got, StatusDone)
	}
	if n := len(f.SnapshotAll()); n != 1 {
		t.Fatalf("finished agent left the swarm (%d listed) — the exclusion below would pass vacuously", n)
	}
	u, n := f.InFlightSpend()
	if n != 0 {
		t.Errorf("agents = %d, want 0 once finished", n)
	}
	if u != (provider.Usage{}) {
		t.Errorf("usage = %+v, want zero — a finished child is the recap's to book", u)
	}
}

// A nil swarm is the ordinary case in hosts that never wire one (bot mode, a
// sub-agent's own status), so every caller can stay unconditional.
func TestInFlightSpendOnNilSwarm(t *testing.T) {
	var f *Swarm
	if u, n := f.InFlightSpend(); n != 0 || u != (provider.Usage{}) {
		t.Errorf("nil swarm reported %d agents / %+v", n, u)
	}
}
