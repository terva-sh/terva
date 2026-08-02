package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
)

// newTrustedWorkspace is a host whose cwd is trusted, which is the gate the
// provenance record sits behind (a restricted host is refused at swarm_spawn,
// so there is no lease to narrate).
func newTrustedWorkspace(repo string) *Workspace {
	w := &Workspace{cwd: repo, sessions: map[string]*wsSession{}}
	w.trusted.Store(true)
	return w
}

// noteSpy captures what the host surface would be showing right now — the LAST
// message per key, which is exactly what a replace-by-key sink renders.
type noteSpy struct {
	mu     sync.Mutex
	byKey  map[string]string
	writes int
}

func (s *noteSpy) sink(key, msg, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKey == nil {
		s.byKey = map[string]string{}
	}
	s.byKey[key] = msg
	s.writes++
}

func (s *noteSpy) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[swarmWorktreeNoteKey]
}

// Tested THROUGH the real carrier — acquire and the lease's own Release hook —
// because the retract lives on the hook the swarm invokes, not on a method
// anyone calls directly. A unit test of releaseSwarmLease would pass with the
// hook unwired, which is precisely the bug.
func TestTheLeaseRetractsItsOwnNoteWhenTheAgentFinishes(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	var leases []swarm.WorktreeLease
	for _, id := range []string{"a1", "a2", "a3"} {
		l, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: id, Task: "work"})
		if err != nil {
			t.Fatalf("acquire %s: %v", id, err)
		}
		leases = append(leases, l)
	}

	live := spy.current()
	if !strings.Contains(live, "3 swarm worktrees leased") {
		t.Fatalf("three leases did not fold into one record:\n%s", live)
	}

	for i, l := range leases {
		if l.Release == nil {
			t.Fatalf("lease %d has no release hook", i)
		}
		l.Release()
	}

	done := spy.current()
	if strings.Contains(done, "running without") {
		t.Errorf("every agent finished and the note still says they are running:\n%s", done)
	}
	if !strings.Contains(done, "released, kept for review") {
		t.Errorf("the note never flipped to the past tense:\n%s", done)
	}
	// Still ONE note, not a second one appended beside the stale first.
	if len(spy.byKey) != 1 {
		t.Errorf("the record spread across %d keys, want 1", len(spy.byKey))
	}
}

// A partly-finished swarm is the state the old notice could not express at all.
func TestTheNoteCountsDownAsAgentsFinish(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	l1, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a2", Task: "two"}); err != nil {
		t.Fatal(err)
	}
	l1.Release()

	// One of two done: still live, so still the present tense, and still both
	// worktrees — the released one is kept for review and its path still matters.
	got := spy.current()
	if !strings.Contains(got, "running without") {
		t.Errorf("one agent still running, but the note is already past-tense:\n%s", got)
	}
	if !strings.Contains(got, "2 swarm worktrees") {
		t.Errorf("the released worktree was dropped from the record:\n%s", got)
	}
}

// A second swarm later in the session reports ITS count. Carrying the first
// batch forward would claim worktrees this swarm never leased.
func TestASecondSwarmStartsItsOwnCount(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	first, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "one"})
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	if _, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "b1", Task: "later"}); err != nil {
		t.Fatal(err)
	}
	got := spy.current()
	if !strings.Contains(got, "1 swarm worktree ") {
		t.Errorf("the second swarm inherited the first's count:\n%s", got)
	}
}

// The release hook is exactly-once by contract, but this is host state mutated
// from swarm goroutines. A negative count would render "released" over a note
// that is in fact still live — the original bug, inverted.
func TestAnExtraReleaseCannotDriveTheCountNegative(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	l, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "one"})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release()
	w.releaseSwarmLease()

	if w.swarmLive != 0 {
		t.Errorf("live count = %d, want 0", w.swarmLive)
	}
	if got := spy.current(); !strings.Contains(got, "released, kept for review") {
		t.Errorf("note went wrong after extra releases:\n%s", got)
	}
}

// A host with no replace-by-key surface (the web and ACP daemons) still gets
// the record, on the appending channel it does have. Silence there would be a
// regression: this is a trust-boundary disclosure.
func TestAHostWithoutAReplaceableNoteStillGetsTheRecord(t *testing.T) {
	repo := newCarrierRepo(t)
	var mu sync.Mutex
	var lines []string
	w := newTrustedWorkspace(repo)
	w.SetDiag(func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() })

	l, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "one"})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()

	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 {
		t.Fatalf("diag host saw %d lines, want the lease and the release", len(lines))
	}
	if !strings.Contains(lines[0], "leased") {
		t.Errorf("first line is not the lease:\n%s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "released, kept for review") {
		t.Errorf("last line is not the release:\n%s", lines[len(lines)-1])
	}
}
