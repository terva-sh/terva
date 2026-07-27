package swarm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// newIDCollisionSwarm builds a swarm whose clock never advances, so every
// newAgentID mint lands on the identical "<slug>-<nano>" value — the
// deterministic form of the collision a loaded CI runner produced by chance
// (UnixNano()%1e6 repeats every millisecond).
func newIDCollisionSwarm(t *testing.T) *Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	frozen := time.Unix(1700000000, 123456)
	return New(Config{
		Root:     root,
		RepoRoot: root,
		Now:      func() time.Time { return frozen },
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
}

// TestSpawnNeverReusesAgentIDs pins the fix for the id-collision orphan: two
// spawns minting the same "<slug>-<nano>" overwrote one map entry, leaving an
// agent invisible to /swarm, unreachable by StopAll (its runner ran forever —
// caught as a 30m test-binary timeout under CI-grade load), and sharing its
// state dir with its successor. Every id must be unique and every agent must
// stay individually reachable and stoppable.
func TestSpawnNeverReusesAgentIDs(t *testing.T) {
	f := newIDCollisionSwarm(t)
	defer f.StopAll()

	agents := make([]*Agent, 3)
	seen := map[string]bool{}
	for i := range agents {
		a, err := f.Spawn(context.Background(), "same task text")
		if err != nil {
			t.Fatal(err)
		}
		agents[i] = a
		if seen[a.ID] {
			t.Fatalf("agent id %q minted twice", a.ID)
		}
		seen[a.ID] = true
	}
	if got := len(f.List()); got != 3 {
		t.Fatalf("List() = %d agents, want 3 (an overwritten map entry orphans an agent)", got)
	}
	for _, a := range agents {
		if f.Get(a.ID) != a {
			t.Fatalf("Get(%q) does not resolve its own agent", a.ID)
		}
	}

	// The orphan symptom itself: StopAll must reach every agent, and every
	// Wait must return. Pre-fix, the overwritten agent's runner was never
	// cancelled and this hung forever.
	f.StopAll()
	for _, a := range agents {
		done := make(chan struct{})
		go func() { a.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("agent %q was never stopped (orphaned by an id collision)", a.ID)
		}
	}
}

// TestSpawnSkipsLeftoverStateDirs pins the on-disk half: an agents/<id>/ dir
// left by a prior process (crashed, or simply not reloaded) must not be
// adopted by a new spawn — that would clobber its meta.json and interleave
// two agents' event logs in one file.
func TestSpawnSkipsLeftoverStateDirs(t *testing.T) {
	f := newIDCollisionSwarm(t)
	defer f.StopAll()

	// The frozen clock makes the first mint predictable: precompute it and
	// squat its state dir before spawning.
	wouldBe := newAgentID("same task text", f.cfg.Now())
	if err := os.MkdirAll(f.agentStateDir(wouldBe), 0o700); err != nil {
		t.Fatal(err)
	}

	a, err := f.Spawn(context.Background(), "same task text")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == wouldBe {
		t.Fatalf("spawn reused the leftover state dir id %q", a.ID)
	}
}

// TestFailedSpawnReleasesClaimedID pins the reservation lifecycle: a spawn
// that fails AFTER minting its id (here: the worktree acquire errors) must
// release the claim, or every retry re-collides with the ghost reservation
// and the suffix chain grows forever.
func TestFailedSpawnReleasesClaimedID(t *testing.T) {
	root := testsupport.TempDir(t)
	frozen := time.Unix(1700000000, 123456)
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		Now:      func() time.Time { return frozen },
		AcquireWorktree: func(context.Context, WorktreeReq) (WorktreeLease, error) {
			return WorktreeLease{}, context.DeadlineExceeded
		},
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
		},
	})
	defer f.StopAll()

	if _, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "doomed"}); err == nil {
		t.Fatal("a failing worktree acquire must fail the spawn")
	}
	f.mu.Lock()
	leaked := len(f.claimed)
	f.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("%d claimed id(s) leaked by a failed spawn", leaked)
	}
}

// TW-041. An id is slugged from the task, and a fan-out shares its prompt
// preamble by construction — that is what makes it a fan-out. taskSlug caps at
// 24 characters, so six agents off one preamble minted six ids differing only
// in the entropy suffix: unique, and unreadable.
//
// The cost was not cosmetic. The id is the handle for the state dir, the
// workflow journal's agent_id, and session_inspect's sub-agent argument — so an
// unreadable id turned an available tool into an invisible one, and a real run
// scraped a /tmp log with Python instead of reading transcripts.
func TestLabelNamesTheAgentID(t *testing.T) {
	f := newIDCollisionSwarm(t)
	defer f.StopAll()

	preamble := "You are one member of an independent review panel. Review slice: "
	a, err := f.SpawnReq(context.Background(), SpawnRequest{
		Task:  preamble + "the core engine",
		Label: "core-engine",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.ID, "core-engine-") {
		t.Errorf("id %q is not named from the label", a.ID)
	}
	if strings.HasPrefix(a.ID, "you-are-one-member") {
		t.Errorf("id %q is still slugged from the shared preamble", a.ID)
	}
}

// An unlabelled spawn must behave exactly as before — the whole change is which
// text the id is NAMED from, for callers that supply one.
func TestUnlabelledAgentIDIsUnchanged(t *testing.T) {
	f := newIDCollisionSwarm(t)
	defer f.StopAll()

	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "review the core engine"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.ID, "review-the-core-engine-") {
		t.Errorf("unlabelled id %q is no longer slugged from the task", a.ID)
	}
}

// Labels are the script author's, so two slices can share one. The id must stay
// unique regardless: claimAgentID re-mints under the swarm lock, and this swarm's
// clock is frozen so the entropy suffix cannot save it.
func TestCollidingLabelsStillMintDistinctIDs(t *testing.T) {
	f := newIDCollisionSwarm(t)
	defer f.StopAll()

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		a, err := f.SpawnReq(context.Background(), SpawnRequest{
			Task:  "slice " + string(rune('a'+i)),
			Label: "same-label",
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[a.ID] {
			t.Fatalf("two agents share id %q — a label collision became a state-dir collision", a.ID)
		}
		seen[a.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("want 3 distinct ids, got %d", len(seen))
	}
}
