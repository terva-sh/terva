package swarm

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

	"terva.sh/terva/packages/testsupport"
)

// TestRunnerEndToEndWithStubChild is the integration test for the
// new daemon-mode runner. It compiles the stubchild binary in
// testdata/cmd/stubchild, points an execRunner at it, and drives
// a Swarm through one Spawn + one SendUserTurn + Stop cycle.
//
// What this proves:
//
//   - The default argv shape (swarmAgentArgs) is one the child can
//     actually parse — locks the shape against silent breakage.
//   - The stdout JSONL parser ingests events and writes them to the
//     durable log.
//   - applyEventToSink turns events into Activity / Transcript
//     updates the dashboard reads.
//   - The supervisor inbox dials the child's socket and a follow-up
//     line round-trips back as a user_message event.
//   - Stop closes the inbox AND cancels the child's context so the
//     stub exits cleanly.
//
// The test is skipped on platforms without unix sockets (Windows).
func TestRunnerEndToEndWithStubChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported")
	}
	if testing.Short() {
		t.Skip("skip end-to-end runner test in -short mode")
	}

	exe := buildStubChild(t)

	root := testsupport.TempDir(t)
	repo := testsupport.TempDir(t)
	f := New(Config{
		Root:     root,
		RepoRoot: repo,
		NewRunner: func(a *Agent) Runner {
			return &execRunner{
				agent: a,
				Command: swarmAgentArgs(swarmAgentArgsOpts{
					Exe:         exe,
					Dir:         a.Dir,
					SessionPath: a.SessionPath,
					InboxPath:   a.InboxPath,
					Task:        a.Task,
					Model:       a.Model,
					Provider:    a.Provider,
				}),
			}
		},
	})
	defer f.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, err := f.Spawn(ctx, "first task")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Wait until the durable log has at least one assistant_message
	// from the initial task. That confirms stdout→log→follower.
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			evs, _ := ReadEventLog(a.EventLogPath)
			for _, ev := range evs {
				if strings.Contains(eventText(ev), want) {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		evs, _ := ReadEventLog(a.EventLogPath)
		t.Fatalf("timed out waiting for %q in event log; got %d events:\n%s\n%s",
			want, len(evs), formatEvents(evs), dumpEventsVerbose(evs))
	}
	waitFor("echo: first task")

	// Send a follow-up over the inbox. The stub echoes the text into
	// an assistant_message we can poll for in the log.
	if err := retrySend(f, a.ID, "user follow up", time.Second); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	waitFor("echo: follow up")

	// Shut the agent down gracefully via the inbox.
	if err := f.SendInput(a.ID, "shutdown"); err != nil && !errors.Is(err, ErrNotReady) {
		t.Fatalf("shutdown send: %v", err)
	}
	a.Wait()
	if got := a.Status(); got != StatusDone && got != StatusKilled {
		t.Fatalf("final status = %s; want done/killed", got)
	}
}

// retrySend exists because the inbox dial races against the child
// opening the socket. Production callers handle ErrNotReady with a
// status message; tests retry within a small window.
func retrySend(f *Swarm, id, msg string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := f.SendInput(id, msg)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, ErrNotReady) {
			return err
		}
		time.Sleep(30 * time.Millisecond)
	}
	return lastErr
}

func eventText(ev Event) string {
	if ev.Type != "assistant_message" && ev.Type != "user_message" {
		return ""
	}
	content, _ := ev.Data["content"].([]any)
	var sb strings.Builder
	for _, c := range content {
		m, _ := c.(map[string]any)
		if t, _ := m["type"].(string); t == "text" {
			if txt, _ := m["text"].(string); txt != "" {
				sb.WriteString(txt)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

func dumpEventsVerbose(evs []Event) string {
	var sb strings.Builder
	for _, ev := range evs {
		sb.WriteString(ev.Type)
		sb.WriteString("\t")
		for k, v := range ev.Data {
			sb.WriteString(k)
			sb.WriteString("=")
			switch vv := v.(type) {
			case string:
				sb.WriteString(vv)
			default:
				sb.WriteString("<...>")
			}
			sb.WriteString(" ")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func formatEvents(evs []Event) string {
	var sb strings.Builder
	for _, ev := range evs {
		sb.WriteString(ev.Type)
		sb.WriteString(" ")
		sb.WriteString(ev.Time.Format(time.RFC3339Nano))
		sb.WriteString("\n")
	}
	return sb.String()
}

func buildStubChild(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "stubchild")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/stubchild")
	// Pass through the test runner's env so `go build` can find
	// HOME, PATH, GOCACHE, etc. CGO is disabled to keep the build
	// hermetic across machines.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stubchild: %v\n%s", err, b)
	}
	return out
}
