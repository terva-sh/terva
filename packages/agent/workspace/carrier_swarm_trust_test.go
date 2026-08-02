package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
)

// The note's action line follows the CURRENT trust store, while the
// RESTRICTED clause keeps the lease-time verdict: a grant cannot reach an
// agent that already booted, so "running without extensions" stays true even
// once "run terva trust" stops being the right ask.
func TestSwarmNoteGrantHintFollowsTheLiveVerdict(t *testing.T) {
	batch := []worktreeProvenance{
		{Path: "/wt/a", Trusted: false, TrustedNow: false},
		{Path: "/wt/b", Trusted: false, TrustedNow: false},
	}

	before := renderSwarmWorktreeNote(batch, 2)
	if !strings.Contains(before, "terva trust") {
		t.Fatalf("ungranted batch lost its hint:\n%s", before)
	}
	if strings.Contains(before, "now trusted") {
		t.Fatalf("ungranted batch already acknowledges a grant:\n%s", before)
	}

	// Half granted: the hint narrows to the path still worth granting.
	batch[0].TrustedNow = true
	half := renderSwarmWorktreeNote(batch, 2)
	if !strings.Contains(half, "terva trust /wt/b") {
		t.Errorf("remaining ungranted path missing from the hint:\n%s", half)
	}
	if strings.Contains(half, "terva trust /wt/a") {
		t.Errorf("granted path still hinted:\n%s", half)
	}
	if strings.Contains(half, "now trusted") {
		t.Errorf("acknowledgment fired with a path still ungranted:\n%s", half)
	}

	// Fully granted, agents still running: the acknowledgment replaces the
	// hint, and the RESTRICTED clause keeps telling the boot-time truth.
	batch[1].TrustedNow = true
	live := renderSwarmWorktreeNote(batch, 2)
	if strings.Contains(live, "terva trust") {
		t.Errorf("hint survived a full grant:\n%s", live)
	}
	if !strings.Contains(live, "now trusted") || !strings.Contains(live, "stay restricted") {
		t.Errorf("live acknowledgment missing or missing its caveat:\n%s", live)
	}
	if !strings.Contains(live, "running without") {
		t.Errorf("boot-time restriction clause was rewritten by the grant:\n%s", live)
	}

	// Fully granted and fully released: same ONE tense switch drives the
	// acknowledgment, so it flips to what the grant means for the future.
	done := renderSwarmWorktreeNote(batch, 0)
	if !strings.Contains(done, "the next swarm here runs with them") {
		t.Errorf("released acknowledgment missing:\n%s", done)
	}
	if strings.Contains(done, "stay restricted") {
		t.Errorf("released note still talks about running agents:\n%s", done)
	}
}

// A batch with nothing restricted has nothing to acknowledge — TRUSTED already
// says it all, and a stray "now trusted" would imply it once wasn't.
func TestSwarmNoteTrustedBatchNeverAcknowledges(t *testing.T) {
	batch := []worktreeProvenance{{Path: "/wt/a", Trusted: true, TrustedNow: true}}
	got := renderSwarmWorktreeNote(batch, 1)
	if strings.Contains(got, "now trusted") {
		t.Errorf("trusted-from-the-start batch acknowledges a grant:\n%s", got)
	}
	if !strings.Contains(got, "TRUSTED") {
		t.Errorf("trusted batch lost its verdict:\n%s", got)
	}
}

// Through the real carrier: lease restricted, grant the path the note's own
// hint names, refresh — the note stops asking and starts acknowledging.
func TestRefreshRewritesTheNoteAfterAGrant(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	lease, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "work"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := spy.current(); !strings.Contains(got, "terva trust") {
		t.Fatalf("restricted lease carried no grant hint:\n%s", got)
	}

	// The exact gesture the hint suggests, from "another process"'s point of
	// view: a store write, not a Workspace method call.
	if err := config.TrustPath(filepath.Dir(lease.Dir), true); err != nil {
		t.Fatalf("trust: %v", err)
	}
	w.refreshSwarmWorktreeTrust()

	got := spy.current()
	if strings.Contains(got, "terva trust") {
		t.Errorf("hint survived the grant it asked for:\n%s", got)
	}
	if !strings.Contains(got, "now trusted") {
		t.Errorf("grant went unacknowledged:\n%s", got)
	}
	if !strings.Contains(got, "running without") {
		t.Errorf("boot-time restriction clause vanished:\n%s", got)
	}

	// An untrust flips it straight back to asking.
	if err := config.UntrustPath(filepath.Dir(lease.Dir)); err != nil {
		t.Fatalf("untrust: %v", err)
	}
	w.refreshSwarmWorktreeTrust()
	if got := spy.current(); !strings.Contains(got, "terva trust") {
		t.Errorf("hint did not return after the untrust:\n%s", got)
	}
}

// A refresh that changes nothing must not re-post the note: a keyed note the
// user dismissed with Esc may only come back for a real state change.
func TestRefreshWithoutAChangeStaysSilent(t *testing.T) {
	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)

	if _, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "work"}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	writes := func() int { spy.mu.Lock(); defer spy.mu.Unlock(); return spy.writes }
	before := writes()
	w.refreshSwarmWorktreeTrust()
	if got := writes(); got != before {
		t.Errorf("no-change refresh re-posted the note (%d -> %d writes)", before, got)
	}
}

// End to end on the wire that will actually carry it: the watcher's mtime poll
// notices an out-of-process store write and rewrites the note unprompted.
func TestTrustWatcherNoticesAStoreWrite(t *testing.T) {
	old := swarmTrustPollInterval
	swarmTrustPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { swarmTrustPollInterval = old })

	repo := newCarrierRepo(t)
	spy := &noteSpy{}
	w := newTrustedWorkspace(repo)
	w.SetNoteSink(spy.sink)
	w.ctx, w.cancel = context.WithCancel(context.Background())
	t.Cleanup(w.cancel)

	lease, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "work"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := config.TrustPath(filepath.Dir(lease.Dir), true); err != nil {
		t.Fatalf("trust: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(spy.current(), "now trusted") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher never noticed the grant; note still:\n%s", spy.current())
}
