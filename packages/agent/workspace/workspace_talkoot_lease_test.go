package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
)

// fakeLeases is a memberLeaser that records each acquire and release.
type fakeLeases struct {
	mu       sync.Mutex
	err      error
	acquired []string
	released []string
	// none lists the members that hold no worktree.
	none []string
}

func (l *fakeLeases) acquire(talkootID, member string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return "", l.err
	}
	l.acquired = append(l.acquired, member)
	return "/leases/" + talkootID + "/" + member, nil
}

func (l *fakeLeases) release(talkootID, member string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, member)
}

func (l *fakeLeases) held(_, member string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !slices.Contains(l.none, member)
}

func (l *fakeLeases) snapshot() (acquired, released []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.acquired), slices.Clone(l.released)
}

// worktreeCrew starts the crew talkoot with jev in a worktree, on fake
// workers and fake leases.
func worktreeCrew(t *testing.T) (*Workspace, *fakeWorkers, *fakeLeases, string) {
	t.Helper()
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw, fl := newFakeWorkers(), &fakeLeases{}
	w.talkootWorkers, w.talkootLeases = fw, fl
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, "    workspace: worktree\n")); err != nil {
		t.Fatal(err)
	}
	return w, fw, fl, cwd
}

// A worker member in a worktree spawns in its member's lease, and the swarm
// leases nothing of its own for it.
func TestAWorktreeMemberSpawnsInItsLease(t *testing.T) {
	w, fw, _, _ := worktreeCrew(t)
	m, _ := memberOf(*w.talkoot.runs["crew"].roster.Load(), "jev")
	if err := w.memberUnbound(m); err != nil {
		t.Fatalf("a worktree member is unbound: %v", err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	spawned, _, _, _ := fw.snapshot()
	if spawned[0].Dir != "/leases/crew/jev" || spawned[0].Approval != "auto-edit" {
		t.Errorf("spawn dir %q posture %q, want the member's lease under auto-edit", spawned[0].Dir, spawned[0].Approval)
	}
}

// A lease that cannot be acquired fails the delivery. The member takes no
// turn, the room says why, and no worker runs in the home checkout.
func TestAFailedLeaseFailsTheDelivery(t *testing.T) {
	w, fw, fl, _ := worktreeCrew(t)
	fl.err = errors.New("worktree \"talkoot-crew-jev\" is claimed by someone else")
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	var refused []talkoot.Line
	waitTalkoot(t, "the refusal", func() bool {
		refused = roomLines(t, "crew", func(l talkoot.Line) bool {
			return l.Type == talkoot.LineGuard && l.Member == "jev" && strings.Contains(l.Reason, "worktree could not be acquired")
		})
		return len(refused) > 0
	})
	if s, _, _, _ := fw.snapshot(); len(s) != 0 {
		t.Errorf("spawned %+v without the member's lease", s)
	}
}

// The binding reads the member as the roster holds it now. A delivery
// routed while the member was in the home checkout, after an update moved it
// to a worktree, takes the lease.
func TestTheBindingLeasesForTheCurrentMember(t *testing.T) {
	w, fw, fl, _ := worktreeCrew(t)
	fl.err = errors.New("no lease")
	run := w.talkoot.runs["crew"]
	stale, _ := memberOf(*run.roster.Load(), "jev")
	stale.Workspace = talkoot.WorkspaceShared
	if err := w.deliverWorker(run, stale, "Start."); err == nil || !strings.Contains(err.Error(), "no lease") {
		t.Errorf("delivery = %v, want the current member's lease refused", err)
	}
	if s, _, _, _ := fw.snapshot(); len(s) != 0 {
		t.Errorf("spawned %+v in the home checkout", s)
	}
}

// A revival, in this process or after a restart, runs in the lease the
// member acquires again.
func TestARevivedWorktreeMemberRunsInItsLease(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()

	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers, w2.talkootLeases = fw, fl
	w2.LoadTalkoots()
	if _, err := w2.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Continue.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, _, _, r := fw.snapshot(); return len(r) == 1 })
	fw.mu.Lock()
	dirs := slices.Clone(fw.resumedIn)
	fw.mu.Unlock()
	if len(dirs) != 1 || dirs[0] != "/leases/crew/jev" {
		t.Errorf("revived in %v, want the member's lease", dirs)
	}
	if acquired, _ := fl.snapshot(); len(acquired) != 2 {
		t.Errorf("acquired %v, want the lease at the spawn and again at the revival", acquired)
	}
	if s, _, _, _ := fw.snapshot(); len(s) != 1 {
		t.Errorf("spawned %d workers, want the one revived", len(s))
	}
}

// A member that leaves its worktree gives it back once its worker stopped.
// One whose worker is only retired keeps it.
func TestAMemberGivesBackTheWorktreeItLeaves(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	edit := func(jev string) {
		t.Helper()
		if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, jev)); err != nil {
			t.Fatal(err)
		}
	}
	// A posture change retires the worker and keeps the lease.
	edit("    workspace: worktree\n    posture: plan\n")
	waitTalkoot(t, "the retirement", func() bool { _, _, stopped, _ := fw.snapshot(); return len(stopped) == 1 })
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Errorf("a retired worker's member gave back its worktree: %v", released)
	}
	// A move to the home checkout gives it back.
	edit("    posture: plan\n")
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return len(r) == 1 && r[0] == "jev" })
}

// A worktree waits for its worker to stop before it is given back, and a
// member that returns to its worktree meanwhile keeps it.
func TestALeaseWaitsForItsWorkerAndTheRoster(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	fw.stopKeepsLive = true
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the stop", func() bool { _, _, stopped, _ := fw.snapshot(); return len(stopped) == 1 })
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Fatalf("the worktree went back while its worker still ran: %v", released)
	}
	// The member returns to its worktree before the worker stops.
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, "    workspace: worktree\n")); err != nil {
		t.Fatal(err)
	}
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()
	time.Sleep(400 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Errorf("a member back in its worktree lost it: %v", released)
	}
}

// The workspace's own leases acquire the member's worktree in the repository,
// find the same one again, and remove it when it holds no work. The claim's
// owner names the talkoot and the member, and the engine reads a claim with
// its own owner as its own whatever process holds it, which is what finds the
// worktree again after a restart.
func TestGitLeasesAcquireTheSameWorktreeAgain(t *testing.T) {
	repo := newCarrierRepo(t)
	w := &Workspace{cwd: repo, ctx: context.Background()}
	g := gitLeases{w: w}
	dir, err := g.acquire("crew", "jev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(dir), "talkoot-crew-jev") {
		t.Errorf("worktree %q, want it named for the member", dir)
	}
	if again, err := g.acquire("crew", "jev"); err != nil || again != dir {
		t.Errorf("a second acquire = %q, %v; want %q", again, err, dir)
	}
	other, err := g.acquire("crew", "tess")
	if err != nil || other == dir {
		t.Errorf("another member's worktree = %q, %v", other, err)
	}
	t.Cleanup(func() { g.release("crew", "tess") })
	g.release("crew", "jev")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a worktree with no work stays after its release: %v", err)
	}
}

// A worker recorded without a lease, for a member the roster now puts in a
// worktree, is not revived in the home checkout. The member gets a new
// worker in its lease.
func TestAnUnleasedWorkerIsNotRevivedForAWorktreeMember(t *testing.T) {
	w, fw, _, _ := worktreeCrew(t)
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	fw.mu.Lock()
	fw.live["agent-1"] = false
	req := fw.reqs["agent-1"]
	req.Dir = ""
	fw.reqs["agent-1"] = req
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "a new worker", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 2 })
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 0 {
		t.Errorf("revived %v, a worker with no lease", resumed)
	}
}

// Two members' worktrees never share a name. The engine folds dashes and
// cuts a name at 64 characters, so ids that differ only there must still
// name different worktrees.
func TestMemberWorktreeNamesDoNotCollide(t *testing.T) {
	long := strings.Repeat("a", 31)
	pairs := [][2][2]string{
		{{"a-b", "c"}, {"a", "b-c"}},
		{{"a--b", "c"}, {"a-b", "c"}},
		{{long + "x", "jev"}, {long + "y", "jev"}},
		{{"crew", long + "x"}, {"crew", long + "y"}},
	}
	for _, p := range pairs {
		x, y := memberWorktreeName(p[0][0], p[0][1]), memberWorktreeName(p[1][0], p[1][1])
		if x == y {
			t.Errorf("%v and %v share the worktree name %q", p[0], p[1], x)
		}
		for _, n := range []string{x, y} {
			if len(n) > 64 || strings.Contains(n, "--") {
				t.Errorf("name %q is one the engine would change", n)
			}
		}
	}
}

// A release waits for every worker that ran in the worktree: one retired by
// an earlier update, and one bound after the member came back and left again.
func TestALeaseWaitsForEveryWorkerThatRanThere(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	fw.stopKeepsLive = true
	post := func(n int) {
		t.Helper()
		if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Go.", nil, ""); err != nil {
			t.Fatal(err)
		}
		waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == n })
		id := fmt.Sprintf("agent-%d", n)
		fw.end(t, id, 0, "")
		waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	}
	edit := func(jev string) {
		t.Helper()
		if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, jev)); err != nil {
			t.Fatal(err)
		}
	}
	quiet := func(why string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		if _, released := fl.snapshot(); len(released) != 0 {
			t.Fatalf("%s: the worktree went back: %v", why, released)
		}
	}
	setLive := func(id string, live bool) {
		fw.mu.Lock()
		fw.live[id] = live
		fw.mu.Unlock()
	}

	post(1)
	// A posture change retires agent-1, which is slow to stop, and a move
	// out follows before it has stopped.
	edit("    workspace: worktree\n    posture: plan\n")
	edit("    posture: plan\n")
	quiet("the retired worker still runs")
	// The member comes back, gets agent-2, and leaves again.
	edit("    workspace: worktree\n    posture: plan\n")
	post(2)
	edit("    posture: plan\n")
	setLive("agent-1", false)
	quiet("agent-2 still runs")
	setLive("agent-2", false)
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return len(r) > 0 })
	// Two releases waited, and only the newer gives the worktree back.
	time.Sleep(400 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 1 {
		t.Errorf("released %v, want the worktree given back once", released)
	}
}

// A worker this run did not record, seated and still live when the member
// leaves, holds the worktree too: a run that loaded while the swarm kept
// the worker has no record of it.
func TestASeatedWorkerHoldsTheWorktreeItLeaves(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	fw.stopKeepsLive = true
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Go.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	run.leaseHolders = nil
	w.talkoot.mu.Unlock()
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Fatalf("the worktree went back under its seated worker: %v", released)
	}
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return len(r) == 1 })
}

// A worker revived into the worktree holds it even when an update retired
// it while it started, and the binding stops it at once.
func TestARevivalThatLostItsSeatHoldsTheWorktree(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Go.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	run.leaseHolders = nil
	w.talkoot.mu.Unlock()
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.stopKeepsLive = true
	fw.onResume = func() {
		w.talkoot.mu.Lock()
		delete(run.seats, "jev")
		w.talkoot.mu.Unlock()
	}
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the stop", func() bool { _, _, stopped, _ := fw.snapshot(); return slices.Contains(stopped, "agent-1") })
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Fatalf("the worktree went back under the revived worker: %v", released)
	}
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return len(r) == 1 })
}

// A worker retired by a change to its limits, while the member stays in its
// worktree, still holds it. A later move out waits for it, even when the run
// had no record of it.
func TestARetiredWorkerStillHoldsTheWorktree(t *testing.T) {
	w, fw, fl, cwd := worktreeCrew(t)
	ctx := t.Context()
	fw.stopKeepsLive = true
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Go.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	run.leaseHolders = nil
	w.talkoot.mu.Unlock()
	edit := func(jev string) {
		t.Helper()
		if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, jev)); err != nil {
			t.Fatal(err)
		}
	}
	edit("    workspace: worktree\n    posture: plan\n")
	edit("    posture: plan\n")
	time.Sleep(300 * time.Millisecond)
	if _, released := fl.snapshot(); len(released) != 0 {
		t.Fatalf("the worktree went back under the retired worker: %v", released)
	}
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return len(r) == 1 })
}
