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
