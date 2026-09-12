package swarm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// newRetentionSwarm builds a swarm whose agents finish immediately, unless the
// task says "forever", in which case they block until cancelled. One swarm can
// then hold both terminal and live agents, which is the mix the sweep has to
// tell apart.
func newRetentionSwarm(t *testing.T) *Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			if strings.Contains(a.Task, "forever") {
				return RunnerFunc(func(ctx context.Context, _ Sink) error {
					<-ctx.Done()
					return ctx.Err()
				})
			}
			return RunnerFunc(func(ctx context.Context, _ Sink) error { return nil })
		},
	})
	t.Cleanup(f.StopAll)
	return f
}

// spawnFinished spawns an agent, waits for it to terminate, and backdates the
// moment it went inert so the sweep sees it as `age` old.
func spawnFinished(t *testing.T, f *Swarm, task string, age time.Duration) *Agent {
	t.Helper()
	a, err := f.Spawn(context.Background(), task)
	if err != nil {
		t.Fatalf("spawn %q: %v", task, err)
	}
	a.Wait()
	a.mu.Lock()
	a.finished = time.Now().Add(-age)
	a.mu.Unlock()
	return a
}

func archived(t *testing.T, f *Swarm, id string) bool {
	t.Helper()
	_, err := os.Stat(f.agentArchiveDir(id))
	return err == nil
}

// terminal() is the whole scope rule, so state it exhaustively rather than
// inferring it from a sweep's behaviour on the four statuses a test happens to
// build.
func TestTerminalCoversExactlyTheInertStatuses(t *testing.T) {
	cases := map[Status]bool{
		StatusDone:     true,
		StatusFailed:   true,
		StatusKilled:   true,
		StatusDetached: true,
		StatusRunning:  false,
		StatusPending:  false,
	}
	for st, want := range cases {
		if got := st.terminal(); got != want {
			t.Errorf("Status(%q).terminal() = %v, want %v", st, got, want)
		}
	}
}

// The sweep's scope: old and terminal is archived, recent is kept, and a live
// agent is kept however long it has been quiet.
func TestSweepArchivesOnlyOldTerminalAgents(t *testing.T) {
	f := newRetentionSwarm(t)

	old := spawnFinished(t, f, "old finished work", 9*24*time.Hour)
	recent := spawnFinished(t, f, "work that just finished", time.Hour)

	// A detached agent is terminal too: nobody resumed it inside the window.
	detached := spawnFinished(t, f, "detached work", 9*24*time.Hour)
	detached.mu.Lock()
	detached.status = StatusDetached
	detached.mu.Unlock()

	// The case the whole scope limit exists for. This agent is live and has
	// been silent for a month, which is exactly the shape of the 27 idle
	// children found on 2026-09-11. Age must not reach it.
	live, err := f.Spawn(context.Background(), "run forever")
	if err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	live.lastEvent = time.Now().Add(-30 * 24 * time.Hour)
	live.mu.Unlock()

	got, errs := f.SweepRetention(7 * 24 * time.Hour)
	if len(errs) != 0 {
		t.Fatalf("sweep reported errors: %v", errs)
	}

	wantArchived := map[string]bool{old.ID: true, detached.ID: true}
	if len(got) != len(wantArchived) {
		t.Fatalf("sweep archived %v, want exactly %v", got, wantArchived)
	}
	for _, id := range got {
		if !wantArchived[id] {
			t.Errorf("sweep archived %s, which it should have left alone", id)
		}
	}
	for id := range wantArchived {
		if !archived(t, f, id) {
			t.Errorf("agent %s has no archive directory", id)
		}
		if f.Get(id) != nil {
			t.Errorf("agent %s is still registered after being archived", id)
		}
	}
	if f.Get(recent.ID) == nil {
		t.Error("a recently finished agent was swept")
	}
	if f.Get(live.ID) == nil {
		t.Error("a live agent was swept, which the sweep must never do")
	}
	if archived(t, f, live.ID) {
		t.Error("a live agent was archived, which the sweep must never do")
	}
}

// The floor is a hard lower bound the configured retention cannot lower, so a
// small value cannot reach an agent that went quiet a couple of hours ago.
func TestSweepFloorOutranksASmallRetention(t *testing.T) {
	f := newRetentionSwarm(t)

	// Inside the floor: two hours old, swept with a one-hour retention. A
	// sweep that honoured the argument literally would take this.
	young := spawnFinished(t, f, "finished two hours ago", 2*time.Hour)
	// Outside the floor, so the same call does take it. Without this the test
	// would pass on a sweep that archived nothing at all.
	older := spawnFinished(t, f, "finished nine hours ago", 9*time.Hour)

	got, errs := f.SweepRetention(time.Hour)
	if len(errs) != 0 {
		t.Fatalf("sweep reported errors: %v", errs)
	}
	if len(got) != 1 || got[0] != older.ID {
		t.Fatalf("sweep archived %v, want only %s", got, older.ID)
	}
	if f.Get(young.ID) == nil {
		t.Errorf("the %v floor did not protect an agent that went quiet 2h ago", RetentionFloor)
	}
}

// Zero or negative retention is the "off" setting, and it must fail safe: a
// caller that lost its configuration sweeps nothing, never everything.
func TestSweepDisabledArchivesNothing(t *testing.T) {
	for _, off := range []time.Duration{0, -time.Hour} {
		f := newRetentionSwarm(t)
		a := spawnFinished(t, f, "ancient work", 400*24*time.Hour)

		got, errs := f.SweepRetention(off)
		if len(got) != 0 || len(errs) != 0 {
			t.Fatalf("SweepRetention(%v) archived %v errs %v, want nothing", off, got, errs)
		}
		if f.Get(a.ID) == nil {
			t.Fatalf("SweepRetention(%v) swept an agent while disabled", off)
		}
	}
}

// A reloaded agent has a terminal status but a ZERO finished, because
// replayEventsIntoAgent sets the status from the event log and never sets that
// field. inertSince falls back to lastEvent for exactly this case. Without the
// fallback a zero time reads as infinitely old and every reloaded agent is
// swept on the first launch after it finished.
func TestSweepDatesAReloadedAgentByItsLastEvent(t *testing.T) {
	f := newRetentionSwarm(t)

	fresh := spawnFinished(t, f, "reloaded, finished an hour ago", 0)
	fresh.mu.Lock()
	fresh.finished = time.Time{} // what Reload leaves behind
	fresh.status = StatusDone
	fresh.lastEvent = time.Now().Add(-time.Hour)
	fresh.mu.Unlock()

	stale := spawnFinished(t, f, "reloaded, last spoke nine days ago", 0)
	stale.mu.Lock()
	stale.finished = time.Time{}
	stale.status = StatusDone
	stale.lastEvent = time.Now().Add(-9 * 24 * time.Hour)
	stale.mu.Unlock()

	got, errs := f.SweepRetention(7 * 24 * time.Hour)
	if len(errs) != 0 {
		t.Fatalf("sweep reported errors: %v", errs)
	}
	if len(got) != 1 || got[0] != stale.ID {
		t.Fatalf("sweep archived %v, want only %s", got, stale.ID)
	}
	if f.Get(fresh.ID) == nil {
		t.Error("a reloaded agent that spoke an hour ago was swept as if it were ancient")
	}
}

// inertSince's ladder, stated directly. The middle rung is the one a refactor
// is most likely to drop, because it only matters for reloaded agents.
func TestInertSincePrefersFinishedThenLastEventThenStarted(t *testing.T) {
	started := time.Now().Add(-72 * time.Hour)
	spoke := time.Now().Add(-48 * time.Hour)
	ended := time.Now().Add(-24 * time.Hour)

	full := &Agent{Started: started}
	full.lastEvent, full.finished = spoke, ended
	if got := full.inertSince(); !got.Equal(ended) {
		t.Errorf("with all three set, inertSince = %v, want finished %v", got, ended)
	}

	reloaded := &Agent{Started: started}
	reloaded.lastEvent = spoke
	if got := reloaded.inertSince(); !got.Equal(spoke) {
		t.Errorf("with no finished, inertSince = %v, want lastEvent %v", got, spoke)
	}

	silent := &Agent{Started: started}
	if got := silent.inertSince(); !got.Equal(started) {
		t.Errorf("with neither, inertSince = %v, want Started %v", got, started)
	}
}
