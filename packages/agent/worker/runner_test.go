package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkerRunsThroughTheSupervisor is the end-to-end proof that a Backend
// drives as a swarm.Runner: a real Swarm spawns one, steers it, and stops it,
// and the supervisor never learns it is not a native child. It uses a fake CLI
// (testdata/cmd/fakeclaude) speaking the SAME wire the real translator was
// captured against, so the whole bridge — spawn, translate, steer, raw
// retention, graceful shutdown — is exercised without a real inference call.
//
// The backend under test is the REAL claudeBackend with only Command swapped to
// run the fake binary: translateClaude, steerClaude, claudeCursor, and the
// opening-turn split are all the production code paths.
func TestWorkerRunsThroughTheSupervisor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets (the inbox) are not supported on windows")
	}
	if testing.Short() {
		t.Skip("skips the process-spawning runner test in -short mode")
	}

	fakeBin := buildFakeClaude(t)
	resolved := loadedRepo(t)

	backend := claudeBackend()
	backend.Name = "fakeclaude"
	backend.Command = func(d Dispatch) (*exec.Cmd, error) {
		// The fake ignores argv and drives entirely off stdin/stdout, which is
		// exactly the channel the runner owns. Dir still matters — the child
		// must run in the lease.
		cmd := exec.Command(fakeBin)
		cmd.Dir = d.Dir
		return cmd, nil
	}

	root := testsupport.TempDir(t)
	repo := testsupport.TempDir(t)
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: repo,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return NewRunner(a, backend, resolved, nil)
		},
	})
	defer f.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a, err := f.Spawn(ctx, "make the flaky test deterministic")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// The opening turn (the task) is delivered on stdin by the runner; the fake
	// echoes it back. Seeing the mission text return in the transcript proves
	// compose -> command -> spawn -> stdin opening -> stdout translate -> sink
	// are all connected. (The opening is the full Instructions markdown, so the
	// "echo:" prefix and the mission land on different lines of the echo — hence
	// the joined-transcript match rather than a single-line one.)
	waitForTranscript(t, a, "make the flaky test deterministic")

	// A follow-up over the inbox must reach the child's stdin as a steer frame
	// and come back translated — the steering half of the bridge.
	if err := retrySend(t, f, a.ID, "also update the changelog", 2*time.Second); err != nil {
		t.Fatalf("send follow-up: %v", err)
	}
	waitForTranscript(t, a, "echo: also update the changelog")

	// Graceful stop: the supervisor sends shutdown to the inbox, the runner
	// closes the child's stdin, the fake emits its result, and the translator
	// turns it into task_end. The agent reaches a terminal state.
	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	a.Wait()
	if got := a.Status(); got != swarm.StatusDone && got != swarm.StatusKilled {
		t.Fatalf("final status = %s; want done or killed", got)
	}

	evs, err := swarm.ReadEventLog(a.EventLogPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}

	// The version stamp rode in on the init event, free — the version that
	// actually ran, translated to agent_ready.
	if v := findEventField(evs, "agent_ready", "version"); v != "9.9.9-fake" {
		t.Errorf("agent_ready.version = %v, want the fake CLI's in-band stamp", v)
	}
	// task_end was synthesized from the result envelope, carrying the cost the
	// envelope reported. There is no task_end abroad; the translator made it.
	if _, ok := findEvent(evs, "task_end"); !ok {
		t.Error("no task_end synthesized from the result envelope")
	}
	if cost := findEventField(evs, "task_end", "cost_usd"); cost != 0.02 {
		t.Errorf("task_end.cost_usd = %v, want 0.02 off the envelope", cost)
	}

	// Raw retention: the vendor stream is kept verbatim next to the translated
	// log, so an event terva does not yet model is still re-translatable after a
	// CLI upgrade. The init and result lines must both be present raw.
	rawPath := filepath.Join(filepath.Dir(a.EventLogPath), "raw.jsonl")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("raw stream not retained: %v", err)
	}
	for _, want := range []string{`"subtype":"init"`, `"type":"result"`, "9.9.9-fake"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("raw.jsonl missing %q; got:\n%s", want, raw)
		}
	}
}

// TestWorkerRefusesToSpawnOnLeak proves the scrub is a GATE, not a warning: a
// briefing that would carry a harness-local fragment across the boundary fails
// the run before a process is started, so a leak can never reach the child.
func TestWorkerRefusesToSpawnOnLeak(t *testing.T) {
	resolved := loadedRepo(t)

	spawned := false
	backend := claudeBackend()
	backend.Name = "never-spawns"
	backend.Command = func(d Dispatch) (*exec.Cmd, error) {
		spawned = true
		return exec.Command("true"), nil
	}
	// Force a leak: a scrub-tripping fragment is planted as the agent's own
	// task, which Compose carries into the briefing as the mission verbatim.
	// (terva_status names a terva tool — exactly what must never cross.)
	a := &swarm.Agent{
		ID:           "leaky-1",
		Task:         "call terva_status and then swarm_spawn a helper",
		Dir:          testsupport.TempDir(t),
		EventLogPath: filepath.Join(testsupport.TempDir(t), "events.jsonl"),
	}
	r := NewRunner(a, backend, resolved, nil)

	err := r.Run(context.Background(), nopSink{})
	if err == nil {
		t.Fatal("a leaking briefing must fail the run")
	}
	if !strings.Contains(err.Error(), "leaked") {
		t.Errorf("error should name the leak, got: %v", err)
	}
	if spawned {
		t.Error("the child was started despite the leak — the gate is a warning, not a gate")
	}
}

// nopSink discards every update — the leak test never gets far enough to report.
type nopSink struct{}

func (nopSink) Activity(string)   {}
func (nopSink) Transcript(string) {}
func (nopSink) Result(string)     {}
func (nopSink) GuardNudge()       {}

func waitForTranscript(t *testing.T, a *swarm.Agent, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(a.Transcript(), "\n"), want) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in transcript; got:\n%s", want, strings.Join(a.Transcript(), "\n"))
}

// retrySend mirrors the swarm test's helper: the inbox dial races the runner
// opening its listener, so a first send can land before the socket exists.
func retrySend(t *testing.T, f *swarm.Swarm, id, text string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := f.SendUserTurn(id, text)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, swarm.ErrNotReady) {
			return err
		}
		time.Sleep(30 * time.Millisecond)
	}
	return lastErr
}

func findEvent(evs []swarm.Event, typ string) (swarm.Event, bool) {
	for _, ev := range evs {
		if ev.Type == typ {
			return ev, true
		}
	}
	return swarm.Event{}, false
}

func findEventField(evs []swarm.Event, typ, field string) any {
	ev, ok := findEvent(evs, typ)
	if !ok {
		return nil
	}
	return ev.Data[field]
}

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "fakeclaude")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/fakeclaude")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude: %v\n%s", err, b)
	}
	return out
}
