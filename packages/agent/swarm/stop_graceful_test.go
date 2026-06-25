package swarm

import (
	"context"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// Stop drives a graceful shutdown through the inbox: the child
// receives the shutdown message, emits agent_stopped, and exits on
// its own — so the backstop context-cancel (StopGrace) never has to
// fire. Reuses the stubchild integration harness.
func TestStopGracefulViaInbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets not supported")
	}
	if testing.Short() {
		t.Skip("skip end-to-end stop test in -short mode")
	}

	exe := buildStubChild(t)
	f := New(Config{
		Root:      testsupport.TempDir(t),
		RepoRoot:  testsupport.TempDir(t),
		StopGrace: 3 * time.Second,
		NewRunner: func(a *Agent) Runner {
			return &execRunner{
				agent: a,
				Command: swarmAgentArgs(swarmAgentArgsOpts{
					Exe:         exe,
					Dir:         a.Dir,
					SessionPath: a.SessionPath,
					InboxPath:   a.InboxPath,
					Task:        a.Task,
				}),
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, err := f.Spawn(ctx, "do a thing")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Let the child boot and open its listener so the shutdown send
	// reaches it (rather than hitting ErrNotReady and falling back to
	// the context-cancel path).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.SendUserTurn(a.ID, "warm up"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stopStart := time.Now()
	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	a.Wait()
	elapsed := time.Since(stopStart)

	// The child exited via the graceful path well inside StopGrace —
	// if the backstop cancel had been needed, this would be ~3s.
	if elapsed >= 3*time.Second {
		t.Errorf("Stop took %v; expected a prompt graceful exit, not the backstop timeout", elapsed)
	}
	if got := a.Status(); got != StatusKilled && got != StatusDone {
		t.Fatalf("final status = %s; want killed/done", got)
	}

	// The child recorded a clean shutdown in its event log.
	evs, _ := ReadEventLog(a.EventLogPath)
	var sawStopped bool
	for _, ev := range evs {
		if ev.Type == "agent_stopped" {
			sawStopped = true
		}
	}
	if !sawStopped {
		t.Errorf("no agent_stopped event in the log; graceful teardown didn't run\n%s", formatEvents(evs))
	}
}
