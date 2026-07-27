package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestSwarmGuardHoldOnce pins the guard's spin-avoidance: it holds a
// finalizing coordinator back exactly ONCE per batch of running sub-agents
// (long enough to stop it racing off), then lets it idle so the queued
// [auto-swarm update] recap re-engages it — never a loop while sub-agents run.
// waitQueuedRecap waits for the batch flusher's recap to LAND in the session
// queue. Waiting for swarmWatch to drain is not enough: flushSwarmSummary
// takes the batch and nils the slice under swarmWatchMu, RELEASES the mutex,
// and only then composes and queues the recap — so "drained" opens a window
// where the queue is still empty. On a loaded CI runner that window is wide
// enough to fail an immediate assertion; the queued message is the effect
// these tests care about, so wait for it.
func waitQueuedRecap(t *testing.T, s *wsSession) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if q := s.agent.PendingQueuedMessages(); len(q) > 0 {
			return q
		}
		if time.Now().After(deadline) {
			s.swarmWatchMu.Lock()
			n := len(s.swarmWatch)
			s.swarmWatchMu.Unlock()
			t.Fatalf("no recap queued within deadline (%d swarm-watch entries still tracked)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSwarmGuardHoldOnce(t *testing.T) {
	s := &wsSession{}

	// No sub-agents tracked → never hold.
	if s.swarmGuardHold() {
		t.Fatal("held with no sub-agents")
	}

	// One running sub-agent → hold once, then stand down (no spin).
	s.swarmWatch = []*swarmWatchEntry{{done: false}}
	if !s.swarmGuardHold() {
		t.Fatal("should hold once while a sub-agent is running")
	}
	if s.swarmGuardHold() {
		t.Fatal("must NOT hold a second time for the same batch (would spin)")
	}

	// A new spawn re-arms the guard (trackSwarmAgent clears the flag).
	s.swarmGuardNudged = false
	if !s.swarmGuardHold() {
		t.Fatal("a new spawn should re-arm the one-shot hold")
	}

	// Once every sub-agent is done, never hold — regardless of the flag.
	s.swarmWatch[0].done = true
	s.swarmGuardNudged = false
	if s.swarmGuardHold() {
		t.Fatal("must not hold once all sub-agents have finished")
	}
}

// TestSwarmWatcherFinalisesOnTerminalCrash isolates the terminal-state
// fallback: a sub-agent that crashes before ever emitting a task-level
// turn_end must still be finalised by the Wait() waiter, so the batch flushes
// and swarmWatch drains — one such zombie would otherwise wedge every future
// recap in the session. (The recap test below has a "doomed" child, but in a
// mixed batch; this pins the wedge case alone. Ported from the deleted
// modes-side twin's test when that file went.)
func TestSwarmWatcherFinalisesOnTerminalCrash(t *testing.T) {
	root := testsupport.TempDir(t)
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
				return errors.New("boom") // no turn_end, straight to terminal state
			})
		},
	})
	defer f.StopAll()

	s := &wsSession{
		agent: core.NewAgent(nil, "fake-model", "", core.Registry{}),
		hub:   &wsHub{},
	}
	s.turnCancel = func() {}

	a, err := f.Spawn(context.Background(), "crash task")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the entry from the tracker itself: reading s.swarmWatch[0] here
	// raced the flush — this crash-fast child can finalise and drain the
	// slice before the next line runs (CI run 1735, index out of range).
	entry := s.trackSwarmAgentEntry(a, "crash task")

	// The Wait() fallback is the only finalise path that can fire here (the
	// crash produces no turn_end); the queued recap is its observable effect.
	q := waitQueuedRecap(t, s)
	if len(q) != 1 || !strings.Contains(q[0], "boom") {
		t.Fatalf("recap = %q, want exactly one entry carrying the crash error", q)
	}

	// A late second finalise (e.g. a straggling turn_end) must be a no-op:
	// the first finaliser won and its outcome stands.
	s.finalizeSwarmEntry(entry, "late")
	if entry.err != "" {
		t.Errorf("idempotent finalise overwrote err: got %q, want unchanged empty", entry.err)
	}
}

// TestCarrierRecapFlowsToSessionQueue drives the full carrier recap path —
// trackSwarmAgent → finalizeSwarmEntry → flushSwarmSummary → s.queue — with
// real swarm agents, pinning the behaviour fixed in 67a690f/d64227e: ONE
// [auto-swarm update] lands on the session queue when the whole batch
// finishes, carrying each sub-agent's task status and actual findings (the
// child's answer via Sink.Result), with a failed child's error surfaced.
func TestCarrierRecapFlowsToSessionQueue(t *testing.T) {
	root := testsupport.TempDir(t)

	// Runners block on release so both agents are tracked before either
	// finishes — one batch, one recap, deterministically.
	release := make(chan struct{})
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			if strings.Contains(a.Snapshot().Task, "doomed") {
				return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
					<-release
					return errors.New("boom: provider exploded")
				})
			}
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
				<-release
				sink.Result("QA findings: two flaky tests in pkg/x")
				return nil
			})
		},
	})
	defer f.StopAll()

	s := &wsSession{
		agent: core.NewAgent(nil, "fake-model", "", core.Registry{}),
		hub:   &wsHub{},
	}
	// A live turnCancel makes s.queue take the QueueMessage branch (queue
	// onto the running turn) instead of starting a real prompt.
	s.turnCancel = func() {}

	a1, err := f.Spawn(context.Background(), "review the QA surface")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := f.Spawn(context.Background(), "doomed side quest")
	if err != nil {
		t.Fatal(err)
	}
	s.trackSwarmAgent(a1, "review the QA surface")
	s.trackSwarmAgent(a2, "doomed side quest")
	close(release)

	q := waitQueuedRecap(t, s)
	if len(q) != 1 {
		t.Fatalf("queued messages = %d, want exactly 1 batch recap: %q", len(q), q)
	}
	recap := q[0]
	for _, want := range []string{
		"[auto-swarm update] 2 sub-agent(s) finished:",
		"status: completed",
		"QA findings: two flaky tests in pkg/x", // Sink.Result → Findings()
		"status: failed",
		"boom: provider exploded",
		"review the QA surface",
		"doomed side quest",
		"full transcript: session_inspect with session_id", // the S1 retrieval handle
	} {
		if !strings.Contains(recap, want) {
			t.Errorf("recap missing %q:\n%s", want, recap)
		}
	}
}
