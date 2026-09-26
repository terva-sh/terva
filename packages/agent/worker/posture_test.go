package worker

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkerPosture pins the resolution priority: an explicit override always
// wins; a leased worker is autonomous (yolo); an unleased one inherits the
// dispatcher's posture. This is the whole policy, in one pure function.
func TestWorkerPosture(t *testing.T) {
	cases := []struct {
		name      string
		override  string
		leased    bool
		inherited string
		want      string
	}{
		{"unleased inherits ask", "", false, "ask", "ask"},
		{"unleased inherits yolo", "", false, "yolo", "yolo"},
		{"unleased inherits workspace", "", false, "workspace", "workspace"},
		{"leased goes yolo despite ask", "", true, "ask", "yolo"},
		{"leased goes yolo despite workspace", "", true, "workspace", "yolo"},
		{"override wins over lease", "workspace", true, "ask", "workspace"},
		{"override wins unleased", "ask", false, "yolo", "ask"},
		{"override wins over yolo-lease", "ask", true, "ask", "ask"},
		{"blank override with whitespace ignored", "   ", true, "ask", "yolo"},
	}
	for _, c := range cases {
		if got := WorkerPosture(c.override, c.leased, c.inherited); got != c.want {
			t.Errorf("%s: WorkerPosture(%q, %v, %q) = %q, want %q", c.name, c.override, c.leased, c.inherited, got, c.want)
		}
	}
}

// TestWorkerPostureReachesDispatch proves the policy end to end through the REAL
// runner + Swarm: the resolved posture is what the backend's Command actually
// receives on the Dispatch. It exercises the whole wire — SpawnRequest.Approval →
// Agent.Approval, AcquireWorktree → Agent.Leased, the runner applying
// WorkerPosture — by capturing d.Briefing.Policy.Posture from a recording backend.
func TestWorkerPostureReachesDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inbox is a unix socket")
	}
	cases := []struct {
		name       string
		dispatcher string // the dispatcher's resolved posture
		override   string // SpawnRequest.Approval
		leased     bool   // whether AcquireWorktree grants a dedicated dir
		want       string
	}{
		{"unleased inherits", "ask", "", false, "ask"},
		{"leased is autonomous", "ask", "", true, "yolo"},
		{"override beats lease", "ask", "workspace", true, "workspace"},
		{"override beats inherit", "yolo", "ask", false, "ask"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spawnAndCapturePosture(t, c.dispatcher, c.override, c.leased); got != c.want {
				t.Errorf("dispatched posture = %q, want %q", got, c.want)
			}
		})
	}
}

// spawnAndCapturePosture spawns one worker through the real Swarm+runner with a
// recording backend and returns the posture its Command was dispatched with.
func spawnAndCapturePosture(t *testing.T, dispatcher, override string, leased bool) string {
	t.Helper()

	// Isolation, and deliberately no config: this test drives leasing through
	// AcquireWorktree's `leased` argument, so the user's own swarm_worktrees
	// setting must not get a second vote in the answer.
	tervaHome(t, "")

	repo := testsupport.TempDir(t)
	r, err := build.Resolve(build.Args{CWD: repo, Approval: dispatcher}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	captured := make(chan string, 1)
	backend := tervaBackend() // self-assembling: no scrub, posture rides --approval
	backend.Name = "recorder"
	backend.Command = func(d Dispatch) (*exec.Cmd, error) {
		select {
		case captured <- d.Briefing.Policy.Posture:
		default:
		}
		// A trivial process: it satisfies the runner's pipes and exits at once.
		return exec.Command("true"), nil
	}

	cfg := swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: repo,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return NewRunner(a, backend, r, nil)
		},
	}
	if leased {
		cfg.AcquireWorktree = func(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
			return swarm.WorktreeLease{Dir: testsupport.TempDir(t), Release: func() {}}, nil
		}
	}
	f := swarm.New(cfg)
	defer f.StopAll()

	if _, err := f.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "do the thing", Approval: override}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case p := <-captured:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("backend Command was never invoked; the runner did not dispatch")
		return ""
	}
}

// TestRevivedLeasedWorkerLosesAutonomyWithItsLease covers a worker revived
// after a daemon restart. Reload moves a detached agent back to RepoRoot, so a
// worker that ran autonomously in its own lease is revived in the operator's
// live checkout. There it must inherit the dispatcher's posture like any
// unleased worker. Before the fix it kept its leased flag and ran yolo in the
// shared tree.
func TestRevivedLeasedWorkerLosesAutonomyWithItsLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inbox is a unix socket")
	}
	tervaHome(t, "")

	repo := testsupport.TempDir(t)
	r, err := build.Resolve(build.Args{CWD: repo, Approval: "ask"}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	type dispatched struct{ posture, dir string }
	captured := make(chan dispatched, 2)
	backend := tervaBackend()
	backend.Name = "recorder"
	backend.Command = func(d Dispatch) (*exec.Cmd, error) {
		captured <- dispatched{d.Briefing.Policy.Posture, d.Dir}
		return exec.Command("true"), nil
	}
	newRunner := func(a *swarm.Agent) swarm.Runner { return NewRunner(a, backend, r, nil) }
	wait := func(what string) dispatched {
		t.Helper()
		select {
		case d := <-captured:
			return d
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: backend Command was never invoked", what)
			return dispatched{}
		}
	}

	root := testsupport.TempDir(t)
	lease := testsupport.TempDir(t)
	first := swarm.New(swarm.Config{
		Root: root, RepoRoot: repo, NewRunner: newRunner,
		AcquireWorktree: func(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
			return swarm.WorktreeLease{Dir: lease, Release: func() {}}, nil
		},
	})
	a, err := first.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "do the thing"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Positive control: the first run really is the autonomous leased case, so
	// a pass below cannot come from a worker that was never leased.
	if d := wait("first run"); d.posture != "yolo" || d.dir != lease {
		t.Fatalf("first run dispatched (%q, %q), want (yolo, %q)", d.posture, d.dir, lease)
	}
	a.Wait()
	first.StopAll()

	// A daemon restart: a fresh Swarm over the same state root.
	second := swarm.New(swarm.Config{Root: root, RepoRoot: repo, NewRunner: newRunner})
	defer second.StopAll()
	if loaded, errs := second.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	if _, err := second.Resume(context.Background(), a.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	d := wait("revival")
	if d.dir != repo {
		t.Fatalf("revival dispatched in %q, want RepoRoot %q", d.dir, repo)
	}
	if d.posture != "ask" {
		t.Errorf("revival in the shared checkout dispatched posture %q, want the dispatcher's %q", d.posture, "ask")
	}
}
