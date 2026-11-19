package workspace

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// countedClient answers each request with one line, and counts the requests.
// A test reads the count to prove that no turn ran.
type countedClient struct{ calls atomic.Int32 }

func (c *countedClient) Name() string { return "counted-fake" }

func (c *countedClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	c.calls.Add(1)
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

// recordSession builds a session over sess the way one daemon instance does:
// boot names the instance, and f is the swarm it owns.
func recordSession(t *testing.T, sess *session.Session, cl provider.Client, boot string, f *swarm.Swarm) *wsSession {
	t.Helper()
	s := &wsSession{
		id:    "record",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}, boot: boot, swarm: f},
		hub:   newWSHub(),
		sess:  sess,
		agent: coretest.NewAgent(cl, "fake-model", "", core.Registry{}),
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	s.agent.AddQueueDrainedObserver(s.queueDrained)
	return s
}

// recordedBatch reads the sub-agent ids that the session's state file holds.
func recordedBatch(t *testing.T, sess *session.Session) session.SwarmBatch {
	t.Helper()
	batch, _ := session.LoadSessionState(sess.StatePath()).SwarmBatch()
	return batch
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// childRunner stands in for a sub-agent's process. It writes the events a
// real child writes, so the next daemon reads the same log.
//
// A task that names "finishes" ends its task and then stays up, as a child
// does. It waits for finish first, so that the test can track the whole
// batch before any part of it ends. Every other task is still at work when
// the daemon stops it. kill leaves the log with no end at all, which is a
// daemon that died.
func childRunner(a *swarm.Agent, finish <-chan struct{}, ready chan<- struct{}, kill bool) swarm.Runner {
	return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
		log, err := swarm.OpenEventLog(a.EventLogPath)
		if err != nil {
			return err
		}
		defer log.Close()
		finishes := strings.Contains(a.Snapshot().Task, "finishes")
		text := "half of the review, and the next file to read"
		if finishes {
			text = "the review found two flaky tests"
		}
		swarm.IngestEvent(swarm.NewEvent("assistant_message", map[string]any{
			"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
		}), log, sink, a)
		if finishes {
			<-finish
			swarm.IngestEvent(swarm.NewEvent("task_end", map[string]any{"step": 1, "error": ""}), log, sink, a)
		}
		ready <- struct{}{}
		<-ctx.Done()
		if kill {
			return ctx.Err()
		}
		if !finishes {
			swarm.IngestEvent(swarm.NewEvent("task_end", map[string]any{"step": 1, "error": "context canceled", "interrupted": true}), log, sink, a)
		}
		swarm.IngestEvent(swarm.NewEvent("agent_stopped", map[string]any{"reason": "shutdown"}), log, sink, a)
		return nil
	})
}

// stoppedDaemon runs one daemon instance to its stop. The session's agent
// starts two sub-agents, one finishes its task, and the daemon shuts down
// while the other is at work. It returns the session file's path and the
// swarm's root, which is all that a restart keeps.
func stoppedDaemon(t *testing.T, kill bool) (sessPath, swarmRoot string, ids []string) {
	t.Helper()
	tmp := testsupport.TempDir(t)
	swarmRoot = testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	// A session with no message is a draft, and its close removes the file.
	// The session that starts sub-agents has the person's request in it.
	if err := sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "review and refactor"}},
		Time:    time.Now(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	ready := make(chan struct{}, 2)
	finish := make(chan struct{})
	f := swarm.New(swarm.Config{
		Root:      swarmRoot,
		RepoRoot:  tmp,
		StopGrace: 10 * time.Millisecond,
		NewRunner: func(a *swarm.Agent) swarm.Runner { return childRunner(a, finish, ready, kill) },
	})
	cl := &countedClient{}
	s := recordSession(t, sess, cl, "first", f)
	// A turn is in progress, as it is when a coordinator starts sub-agents.
	s.turnCancel = func(error) {}

	for _, task := range []string{"a review that finishes", "a long refactor"} {
		a, err := f.SpawnReq(context.Background(), swarm.SpawnRequest{Task: task, SessionID: s.id})
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		s.trackSwarmAgent(a, task)
		ids = append(ids, a.ID)
	}
	close(finish)
	<-ready
	<-ready
	if got := recordedBatch(t, sess); len(got.Agents) != 2 || got.Boot != "first" {
		t.Fatalf("the state file holds %+v after two spawns, want both ids under the first daemon's name", got)
	}

	// What Workspace.Close does to the swarm.
	s.ws.sessions = map[string]*wsSession{s.id: s}
	s.ws.stopSwarm()
	// 🔑 The stop of a sub-agent is not the end of its task. A recap here
	// says "finished" to a session that is going away, and it clears the
	// record that the next daemon needs.
	//
	// ⚠️ Each sub-agent's waiter runs after its runner returns, on its own
	// goroutine. The check gives it time, because a check that runs first
	// passes with the watch still live.
	for deadline := time.Now().Add(150 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if q := s.agent.PendingQueuedMessages(); len(q) != 0 {
			t.Fatalf("the daemon's stop queued a recap into the session it was closing: %q", q[0].Text)
		}
	}
	if got := recordedBatch(t, sess); len(got.Agents) != 2 {
		t.Fatalf("the state file holds %v after the stop, want both sub-agents still on record", got.Agents)
	}
	if n := cl.calls.Load(); n != 0 {
		t.Fatalf("the model was called %d time(s) while the daemon stopped", n)
	}
	path := sess.Path
	if err := sess.Close(); err != nil {
		t.Fatalf("close the session: %v", err)
	}
	return path, swarmRoot, ids
}

// nextDaemon opens the same session under a new daemon instance, with the
// swarm reloaded from disk, and builds the session's restart recap.
func nextDaemon(t *testing.T, sessPath, swarmRoot, boot string) (*wsSession, *countedClient) {
	t.Helper()
	return nextDaemonAfter(t, sessPath, swarmRoot, boot, nil)
}

// nextDaemonAfter is nextDaemon with a step between the reload and the
// session build, which is where a person can act in the tasks pane.
func nextDaemonAfter(t *testing.T, sessPath, swarmRoot, boot string, before func(*swarm.Swarm)) (*wsSession, *countedClient) {
	t.Helper()
	sess, _, err := session.OpenSession(sessPath)
	if err != nil {
		t.Fatalf("open the session again: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	f := swarm.New(swarm.Config{
		Root:      swarmRoot,
		RepoRoot:  testsupport.TempDir(t),
		StopGrace: 10 * time.Millisecond,
		// A sub-agent that a person starts again stays at work.
		NewRunner: func(*swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, _ swarm.Sink) error {
				<-ctx.Done()
				return nil
			})
		},
	})
	t.Cleanup(func() { f.StopAllAndWait(0) })
	if _, errs := f.Reload(); len(errs) > 0 {
		t.Fatalf("reload: %v", errs)
	}
	if before != nil {
		before(f)
	}
	cl := &countedClient{}
	s := recordSession(t, sess, cl, boot, f)
	s.restoreSwarmBatch()
	return s, cl
}

// A daemon that restarts in the middle of a batch leaves a recap that says
// what stopped. The recap waits in the queue and starts no turn, and the
// person's next message takes it into the transcript.
func TestARestartLeavesARecapThatStartsNoTurn(t *testing.T) {
	sessPath, swarmRoot, ids := stoppedDaemon(t, false)
	s, cl := nextDaemon(t, sessPath, swarmRoot, "second")

	q := s.agent.PendingQueuedMessages()
	if len(q) != 1 {
		t.Fatalf("the restart left %d queued text(s), want one recap", len(q))
	}
	if q[0].Source != ctrlproto.QueueSourceSwarmRecap {
		t.Fatalf("the recap's source is %q, want %q", q[0].Source, ctrlproto.QueueSourceSwarmRecap)
	}
	recap := q[0].Text
	for _, want := range []string{
		"[auto-swarm update] terva restarted before the report on 2 sub-agent(s) reached this session. 1 did not finish:",
		"agent " + ids[0] + ", status: completed",
		"findings: the review found two flaky tests",
		"agent " + ids[1] + ", status: stopped before the task finished",
		"last message before the stop: half of the review, and the next file to read",
		"does not start again without a new request",
	} {
		if !strings.Contains(recap, want) {
			t.Errorf("the recap does not say %q:\n%s", want, recap)
		}
	}

	// 🚨 Nobody is present after a restart. The recap must not start a turn.
	time.Sleep(50 * time.Millisecond)
	if s.busyNow() || cl.calls.Load() != 0 || len(s.agent.Messages()) != 0 {
		t.Fatalf("the restart recap started a turn: busy=%v, model calls=%d, messages=%d",
			s.busyNow(), cl.calls.Load(), len(s.agent.Messages()))
	}
	// The queue is in memory, so the record stays until the recap arrives.
	if got := recordedBatch(t, s.sess); len(got.Agents) != 2 || got.Boot != "second" {
		t.Fatalf("the state file holds %+v while the recap waits, want both ids under the second daemon's name", got)
	}

	// The person's next message starts a turn, and the turn takes the recap.
	if err := s.prompt("where were we?", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitUntil(t, "the turn ends", func() bool { return !s.busyNow() && cl.calls.Load() > 0 })
	var delivered bool
	for _, m := range s.agent.Messages() {
		if m.Role == provider.RoleUser && m.Meta[core.MetaSource] == ctrlproto.QueueSourceSwarmRecap {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("the person's turn did not take the recap into the transcript: %d message(s)", len(s.agent.Messages()))
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("the queue holds %d text(s) after the turn, want none", n)
	}
	waitUntil(t, "the record clears", func() bool { return len(recordedBatch(t, s.sess).Agents) == 0 })
}

// A daemon that dies writes no end for a task. The next daemon reads the
// missing end as a stop, and does not report the sub-agent as finished.
func TestADaemonThatDiedReportsEachUnfinishedTask(t *testing.T) {
	sessPath, swarmRoot, ids := stoppedDaemon(t, true)
	s, _ := nextDaemon(t, sessPath, swarmRoot, "second")

	q := s.agent.PendingQueuedMessages()
	if len(q) != 1 {
		t.Fatalf("the restart left %d queued text(s), want one recap", len(q))
	}
	if want := "agent " + ids[1] + ", status: stopped before the task finished"; !strings.Contains(q[0].Text, want) {
		t.Errorf("the recap does not say %q:\n%s", want, q[0].Text)
	}
	if want := "agent " + ids[0] + ", status: completed"; !strings.Contains(q[0].Text, want) {
		t.Errorf("the recap does not say %q:\n%s", want, q[0].Text)
	}
}

// The queue is in memory. A second restart before anybody sent a message
// must report the batch again, or the first restart's recap is lost.
func TestASecondRestartReportsTheBatchAgain(t *testing.T) {
	sessPath, swarmRoot, _ := stoppedDaemon(t, false)
	s, _ := nextDaemon(t, sessPath, swarmRoot, "second")
	if n := s.agent.QueuedMessageCount(); n != 1 {
		t.Fatalf("the first restart left %d queued text(s), want one", n)
	}
	s.stopSwarmWatch()
	if err := s.sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	third, _ := nextDaemon(t, sessPath, swarmRoot, "third")
	q := third.agent.PendingQueuedMessages()
	if len(q) != 1 || !strings.Contains(q[0].Text, "terva restarted before the report on 2 sub-agent(s)") {
		t.Fatalf("the second restart left %d queued text(s), want the recap again", len(q))
	}
}

// A session that the same daemon builds twice was not restarted. An archive
// and its return is this case, and it must not report a restart.
func TestTheSameDaemonReportsNoRestart(t *testing.T) {
	sessPath, swarmRoot, _ := stoppedDaemon(t, false)
	s, _ := nextDaemon(t, sessPath, swarmRoot, "first")
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("the daemon that wrote the record queued %d text(s) for it, want none", n)
	}
	if got := recordedBatch(t, s.sess); len(got.Agents) != 2 {
		t.Fatalf("the record holds %v, want it kept for a real restart", got.Agents)
	}
}

// A person who removes the recap from the queue has read it. The record
// clears, so the next restart does not bring the recap back.
func TestARemovedRecapClearsTheRecord(t *testing.T) {
	sessPath, swarmRoot, _ := stoppedDaemon(t, false)
	s, _ := nextDaemon(t, sessPath, swarmRoot, "second")
	if n := s.agent.QueuedMessageCount(); n != 1 {
		t.Fatalf("the restart left %d queued text(s), want one", n)
	}

	s.setQueue(nil, true)
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("the queue holds %d text(s) after the removal", n)
	}
	if got := recordedBatch(t, s.sess); len(got.Agents) != 0 {
		t.Fatalf("the record holds %v after a person removed the recap, want none", got.Agents)
	}
}

// A recap that waits behind a turn stays on record until the loop takes it,
// and a turn that a person interrupts does not drop it.
func TestAnInterruptedTurnKeepsTheRecap(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	release := make(chan struct{})
	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: tmp,
		NewRunner: func(*swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(_ context.Context, sink swarm.Sink) error {
				<-release
				sink.Result("the answer")
				return nil
			})
		},
	})
	defer f.StopAll()
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := recordSession(t, sess, cl, "first", f)

	if err := s.prompt("start the work", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	a, err := f.Spawn(context.Background(), "one task")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	s.trackSwarmAgent(a, "one task")
	close(release)
	waitQueuedRecap(t, s)
	// The turn is still in its model call, so the recap is in memory alone.
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("the record holds %v while the recap waits behind the turn, want the one sub-agent", got.Agents)
	}

	s.interruptTurn()
	close(cl.release)
	waitUntil(t, "the interrupted turn ends", func() bool { return !s.busyNow() })
	q := s.agent.PendingQueuedMessages()
	if len(q) != 1 || q[0].Source != ctrlproto.QueueSourceSwarmRecap {
		t.Fatalf("the interrupt left %d queued text(s), want the recap kept", len(q))
	}
	// Kept, and it starts no turn. An interrupt must still stop the session.
	time.Sleep(50 * time.Millisecond)
	if s.busyNow() {
		t.Fatal("the kept recap started a turn after the person's interrupt")
	}
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("the record holds %v after the interrupt, want the sub-agent still on record", got.Agents)
	}
}

// A text that a client queues under the recap's source is the client's own.
// A failed turn drops it, as it drops every other text that a person queued.
func TestAClientsTextWithTheRecapSourceIsNotKept(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	if err := s.prompt("start", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	s.queueFrom(ctrlproto.QueueSourceSwarmRecap, "a text that no batch wrote")

	s.interruptTurn()
	close(cl.release)
	waitUntil(t, "the interrupted turn ends", func() bool { return !s.busyNow() })
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("the interrupt kept %d text(s), want the client's text dropped", n)
	}
}

// The whole path, over two real workspaces on one home directory. The first
// starts a batch and closes, and the second builds the session again.
//
// The tests above call the two halves by hand. This one fails if a session
// that closes keeps its watch, or if the session build does not read the
// record.
func TestAWorkspaceRestartReportsTheBatchItStopped(t *testing.T) {
	cwd := talkootHome(t)
	ctx := context.Background()

	first := openTalkootWorkspace(t, cwd)
	// The person's request makes the session and its file. The test provider
	// refuses the request, so the turn ends at once.
	if err := first.Prompt(ctx, "", ctrlproto.PromptParams{Text: "review and refactor"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	s := first.live("")
	if s == nil {
		t.Fatal("the prompt made no session")
	}
	waitUntil(t, "the refused turn ends", func() bool { return !s.busyNow() })

	// The children are stand-ins. They run in a second swarm on the root
	// that the workspace uses, because the workspace's own swarm starts real
	// processes.
	ready := make(chan struct{}, 2)
	finish := make(chan struct{})
	children := swarm.New(swarm.Config{
		Root:      first.swarm.Root(),
		RepoRoot:  cwd,
		StopGrace: 10 * time.Millisecond,
		NewRunner: func(a *swarm.Agent) swarm.Runner { return childRunner(a, finish, ready, false) },
	})
	var ids []string
	for _, task := range []string{"a review that finishes", "a long refactor"} {
		a, err := children.SpawnReq(ctx, swarm.SpawnRequest{Task: task, SessionID: s.id})
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		s.trackSwarmAgent(a, task)
		ids = append(ids, a.ID)
	}
	close(finish)
	<-ready
	<-ready
	before := len(s.agent.Messages())

	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The children stop after the session closed. Each waiter runs after its
	// runner returns, so the check gives them time.
	children.StopAllAndWait(0)
	for deadline := time.Now().Add(150 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if n := s.agent.QueuedMessageCount(); n != 0 {
			t.Fatalf("a sub-agent's stop queued %d text(s) into a session that had closed", n)
		}
		if n := len(s.agent.Messages()); n != before {
			t.Fatalf("a sub-agent's stop added %d message(s) to a session that had closed", n-before)
		}
	}

	second := openTalkootWorkspace(t, cwd)
	if second.boot == "" || second.boot == first.boot {
		t.Fatalf("the two daemon instances have the names %q and %q, want two that differ", first.boot, second.boot)
	}
	again, err := second.resolve(s.id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	snap := again.snapshot()
	if len(snap.Queued) != 1 || len(snap.QueuedSources) != 1 || snap.QueuedSources[0] != ctrlproto.QueueSourceSwarmRecap {
		t.Fatalf("the resumed session's queue is %d text(s) with the sources %q, want one sub-agent recap", len(snap.Queued), snap.QueuedSources)
	}
	for _, want := range []string{
		"terva restarted before the report on 2 sub-agent(s) reached this session. 1 did not finish:",
		"agent " + ids[0] + ", status: completed",
		"agent " + ids[1] + ", status: stopped before the task finished",
	} {
		if !strings.Contains(snap.Queued[0], want) {
			t.Errorf("the recap does not say %q:\n%s", want, snap.Queued[0])
		}
	}
	time.Sleep(50 * time.Millisecond)
	if again.busyNow() {
		t.Fatal("the resumed session started a turn for the recap")
	}

	// The person's next message takes the recap into the transcript, and the
	// record goes. The provider refuses the request, and that changes
	// nothing: the loop takes the queue before it calls the model.
	if err := second.Prompt(ctx, s.id, ctrlproto.PromptParams{Text: "where were we?"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitUntil(t, "the record clears", func() bool {
		batch, _ := session.LoadSessionState(again.sess.StatePath()).SwarmBatch()
		return len(batch.Agents) == 0
	})
	var arrived bool
	for _, m := range again.agent.Messages() {
		if m.Role == provider.RoleUser && m.Meta[core.MetaSource] == ctrlproto.QueueSourceSwarmRecap {
			arrived = true
		}
	}
	if !arrived {
		t.Fatal("the record cleared, and the recap is not in the transcript")
	}
}

// A session can close while its daemon lives, as an archive closes it. A
// sub-agent that ends after that must not report into the closed session.
// The record stays, and the next daemon reports the batch.
func TestAClosedSessionTakesNoRecap(t *testing.T) {
	cwd := talkootHome(t)
	ctx := context.Background()
	w := openTalkootWorkspace(t, cwd)
	if err := w.Prompt(ctx, "", ctrlproto.PromptParams{Text: "review it"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	s := w.live("")
	if s == nil {
		t.Fatal("the prompt made no session")
	}
	waitUntil(t, "the refused turn ends", func() bool { return !s.busyNow() })

	release := make(chan struct{})
	children := swarm.New(swarm.Config{
		Root:     w.swarm.Root(),
		RepoRoot: cwd,
		NewRunner: func(*swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(context.Context, swarm.Sink) error {
				<-release
				return nil
			})
		},
	})
	defer children.StopAll()
	a, err := children.SpawnReq(ctx, swarm.SpawnRequest{Task: "one task", SessionID: s.id})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	s.trackSwarmAgent(a, "one task")
	statePath := s.sess.StatePath()
	before := len(s.agent.Messages())

	s.close()
	close(release)
	a.Wait()
	for deadline := time.Now().Add(150 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if n := s.agent.QueuedMessageCount(); n != 0 {
			t.Fatalf("the sub-agent's end queued %d text(s) into a session that had closed", n)
		}
		if n := len(s.agent.Messages()); n != before {
			t.Fatalf("the sub-agent's end added %d message(s) to a session that had closed", n-before)
		}
	}
	if batch, _ := session.LoadSessionState(statePath).SwarmBatch(); len(batch.Agents) != 1 {
		t.Fatalf("the record holds %v after the session closed, want the sub-agent kept for the next daemon", batch.Agents)
	}
}

// The session books what a batch spent when the batch ends, and once. A
// recap can wait in the queue for a long time, and a daemon that stops while
// it waits must not lose the cost. The recap of one batch is also written
// again after a restart, so the writer itself must book nothing.
func TestTheBatchsSpendIsBookedOnceWhenTheBatchEnds(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	release := make(chan struct{})
	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: tmp,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(_ context.Context, sink swarm.Sink) error {
				<-release
				swarm.IngestEvent(swarm.NewEvent("usage", map[string]any{
					"cumulative": map[string]any{"input": float64(1000), "output": float64(200), "cost_usd": 0.25},
				}), nil, sink, a)
				sink.Result("the answer")
				return nil
			})
		},
	})
	defer f.StopAll()
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := recordSession(t, sess, cl, "first", f)

	if err := s.prompt("start the work", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	a, err := f.Spawn(context.Background(), "one task")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	s.trackSwarmAgent(a, "one task")
	close(release)
	recap := waitQueuedRecap(t, s)
	if !strings.Contains(recap[0], "cost: $0.2500") {
		t.Fatalf("the recap does not name the sub-agent's cost, so the test has no spend to book:\n%s", recap[0])
	}
	// The recap waits behind the turn, and the cost is on the books.
	waitUntil(t, "the spend is booked", func() bool { return s.agent.DelegatedCost().CostUSD > 0 })
	if got := s.agent.DelegatedCost().CostUSD; got != 0.25 {
		t.Fatalf("the session booked $%.4f while the recap waited, want the batch's $0.2500", got)
	}

	// A second write of the recap, which a restart does, books nothing.
	if _, spent := swarmRecapText("again", []swarmRecapRow{{id: a.ID, snap: a.Snapshot()}}, ""); len(spent) != 1 {
		t.Fatalf("the writer returned %d figure(s) for one sub-agent that spent", len(spent))
	}
	// The turn ends, the recap starts the next one, and nothing books again.
	close(cl.release)
	waitUntil(t, "the recap reaches the transcript", func() bool {
		for _, m := range s.agent.Messages() {
			if m.Meta[core.MetaSource] == ctrlproto.QueueSourceSwarmRecap {
				return true
			}
		}
		return false
	})
	waitUntil(t, "the record clears", func() bool { return len(recordedBatch(t, sess).Agents) == 0 })
	if got := s.agent.DelegatedCost().CostUSD; got != 0.25 {
		t.Fatalf("the session's books hold $%.4f for a batch that spent $0.2500", got)
	}
}

// The wait for a recap exists before its text is queued, because an idle
// session takes the text into a turn at once. A client that replaces the
// queue in that gap has removed nothing, so the record must stay.
func TestAQueueReplacedBeforeTheRecapIsQueuedRemovesNothing(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	s := recordSession(t, sess, &countedClient{}, "first", nil)
	recap := core.QueuedMessage{Text: "the report on one sub-agent", Source: ctrlproto.QueueSourceSwarmRecap}

	// The writer has made the wait and has not yet queued the text.
	s.editSwarmBatch(func(ids []string) []string { return appendID(ids, "a-1") })
	s.awaitSwarmRecap(recap.Text, []string{"a-1"})
	s.setQueue(nil, true)
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("a queue that was replaced before the recap was queued cleared the record: %v", got.Agents)
	}
	if !s.swarmRecapWaits(recap) {
		t.Fatal("a queue that was replaced before the recap was queued ended the wait, so an interrupt would drop the recap")
	}

	// The writer queues the text. A person who removes it now has read it.
	s.agent.QueueMessageFrom(recap.Source, recap.Text)
	s.setQueue(nil, true)
	if got := recordedBatch(t, sess); len(got.Agents) != 0 {
		t.Fatalf("the record holds %v after a person removed the queued recap, want none", got.Agents)
	}
}

// The tasks pane belongs to the workspace, so a person can start a stopped
// sub-agent again before the session is built. The recap must say that it is
// at work, and not that it stopped.
func TestASubAgentThatAPersonStartedAgainIsNotReportedAsStopped(t *testing.T) {
	sessPath, swarmRoot, ids := stoppedDaemon(t, false)
	s, _ := nextDaemonAfter(t, sessPath, swarmRoot, "second", func(f *swarm.Swarm) {
		if _, err := f.Resume(context.Background(), ids[1]); err != nil {
			t.Fatalf("resume: %v", err)
		}
	})

	q := s.agent.PendingQueuedMessages()
	if len(q) != 1 {
		t.Fatalf("the restart left %d queued text(s), want one recap", len(q))
	}
	if want := "agent " + ids[1] + ", status: stopped by the restart, and at work again because a person started it again"; !strings.Contains(q[0].Text, want) {
		t.Errorf("the recap does not say %q:\n%s", want, q[0].Text)
	}
	if bad := "agent " + ids[1] + ", status: stopped before the task finished"; strings.Contains(q[0].Text, bad) {
		t.Errorf("the recap says %q about a sub-agent that is at work", bad)
	}
}

// A recap that starts a turn in an idle session has left the queue when that
// turn ends, and its record goes then. A client's text under the same source
// that arrives in between names no batch, and it must clear nothing.
func TestARecapThatStartsATurnSettlesAtTheTurnsEnd(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	release := make(chan struct{})
	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: tmp,
		NewRunner: func(*swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(_ context.Context, sink swarm.Sink) error {
				<-release
				sink.Result("the answer")
				return nil
			})
		},
	})
	defer f.StopAll()
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := recordSession(t, sess, cl, "first", f)

	a, err := f.Spawn(context.Background(), "one task")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	s.trackSwarmAgent(a, "one task")
	close(release)
	// The session is idle, so the recap starts a turn, and the model call
	// holds that turn open.
	<-cl.started
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("the record holds %v while the recap's turn runs, want the sub-agent kept until the turn ends", got.Agents)
	}
	// A client's own text under the recap's source arrives now. It names no
	// batch.
	s.swarmRecapLeft(core.QueuedMessage{Text: "a text that no batch wrote", Source: ctrlproto.QueueSourceSwarmRecap})
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("a client's text under the recap's source cleared the record: %v", got.Agents)
	}

	// A person edits their own queued text while the recap's turn runs. The
	// recap is the turn's prompt, so the queue does not hold it, and that is
	// no removal.
	s.queue("and one more thing")
	s.setQueue([]core.QueuedMessage{{Text: "and another thing"}}, true)
	s.setQueue(nil, true)
	if got := recordedBatch(t, sess); len(got.Agents) != 1 {
		t.Fatalf("a queue.set while the recap's turn ran cleared the record: %v", got.Agents)
	}

	close(cl.release)
	waitUntil(t, "the turn ends", func() bool { return !s.busyNow() })
	waitUntil(t, "the record clears", func() bool { return len(recordedBatch(t, sess).Agents) == 0 })
}

// A sub-agent with no record may have finished before the retention sweep
// took it. The header must not count it as work that did not finish.
func TestASubAgentWithNoRecordIsNotCountedAsUnfinished(t *testing.T) {
	sessPath, swarmRoot, ids := stoppedDaemon(t, false)
	s, _ := nextDaemonAfter(t, sessPath, swarmRoot, "second", func(f *swarm.Swarm) {
		// The stopped one goes, so the one that is left finished its task.
		if err := f.Remove(ids[1]); err != nil {
			t.Fatalf("remove: %v", err)
		}
	})

	q := s.agent.PendingQueuedMessages()
	if len(q) != 1 {
		t.Fatalf("the restart left %d queued text(s), want one recap", len(q))
	}
	recap := q[0].Text
	if want := "agent " + ids[1] + ", status: no record"; !strings.Contains(recap, want) {
		t.Errorf("the recap does not say %q:\n%s", want, recap)
	}
	// The header alone. The instruction below it also speaks of work that
	// did not finish.
	header, _, _ := strings.Cut(recap, "\n")
	if !strings.HasSuffix(header, "reached this session:") {
		t.Errorf("the header claims more than the rows show: %q", header)
	}
	for _, bad := range []string{"did not finish", "Each of them finished"} {
		if strings.Contains(header, bad) {
			t.Errorf("the header says %q, and one sub-agent has no record: %q", bad, header)
		}
	}
}
