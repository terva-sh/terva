package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Stage 2 of docs/proposals/withdraw-cancelled-prompt.md: core takes a
// cancelled-before-it-was-answered prompt out of the LIVE transcript, and the
// daemon has to make the FILE agree.
//
// These run through NewWorkspace/CreateSession rather than the lighter
// newTurnTestSession, because the thing under test IS the buildSession wiring —
// the observer that persists the withdrawal, and the persistence hook that put
// the message on disk in the first place. A harness that registered those itself
// would be testing its own setup.
//
// The assertion is always a COLD RESUME: close the workspace, open a new one on
// the same home and cwd, and read what came back. Checking the in-memory
// transcript would prove nothing here — core already removed it there, and this
// codebase has been bitten before by an in-memory change with no twin in the
// file replay.

// hangingClient opens a stream and never answers, so the turn is alive and
// nothing has been recorded when the cancel arrives.
type hangingClient struct{ started chan struct{} }

func (c *hangingClient) Name() string { return "hanging-fake" }

func (c *hangingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 1)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		select {
		case c.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
	}()
	return out, nil
}

// withdrawFixture stands up a real workspace + session on a temp home, swaps the
// live agent's client for one that hangs, and returns everything a test needs to
// run a turn, stop it, and then resume the session cold.
type withdrawFixture struct {
	w      *Workspace
	s      *wsSession
	id     string
	cwd    string
	client *hangingClient
}

func newWithdrawFixture(t *testing.T) *withdrawFixture {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}
	client := &hangingClient{started: make(chan struct{}, 1)}
	s.agent.SetClientAndModel(client, "fake-model")
	return &withdrawFixture{w: w, s: s, id: info.ID, cwd: cwd, client: client}
}

// runAndStop dispatches a prompt, waits until it is really in flight, stops it
// with stop, and waits for the turn to settle.
func (f *withdrawFixture) runAndStop(t *testing.T, text string, stop func()) {
	t.Helper()
	if err := f.s.prompt(text, nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-f.client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for f.s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never settled after the cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// resumeCold closes the workspace and opens a fresh one on the same home and
// cwd, returning the resumed session's transcript — the honest daemon-restart
// simulation the swipe tests use.
func (f *withdrawFixture) resumeCold(t *testing.T) []provider.Message {
	t.Helper()
	_ = f.w.Close()

	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: f.cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	if _, err := w2.ResumeSession(context.Background(), f.id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	s2 := w2.live(f.id)
	if s2 == nil {
		t.Fatal("resumed session is not live")
	}
	return s2.agent.Messages()
}

func transcriptContains(msgs []provider.Message, want string) bool {
	for _, m := range msgs {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, want) {
				return true
			}
		}
	}
	return false
}

// The whole point of stage 2. Without the delete amend the prompt is gone from
// the live transcript and still in the file, so it walks back in on the next
// reload — which is worse than never having withdrawn it, because the user
// watched it disappear.
func TestAWithdrawnPromptStaysGoneAcrossAReload(t *testing.T) {
	f := newWithdrawFixture(t)
	f.runAndStop(t, "htop", f.s.interruptTurn)

	if msgs := f.s.agent.Messages(); transcriptContains(msgs, "htop") {
		t.Fatalf("core did not withdraw it from the live transcript, so the durable half is not what is under test here: %+v", msgs)
	}

	if msgs := f.resumeCold(t); transcriptContains(msgs, "htop") {
		t.Errorf("the withdrawn prompt came back on reload — the file never learned about it: %+v", msgs)
	}
}

// The other half of the same decision. A restart drain, or any teardown, cancels
// turns without a person having asked for anything; deleting what the user typed
// on the way past would be terva discarding input nobody asked it to discard.
//
// Workspace.Restart can also outlive its own drain (relaunch.Trigger may fail
// after it), so this is not only about a process that is about to exit.
func TestADrainedTurnKeepsThePromptOnDisk(t *testing.T) {
	f := newWithdrawFixture(t)
	f.runAndStop(t, "the prompt a restart interrupted", f.s.cancelTurn)

	// Memory and disk must AGREE here, unlike the withdrawal case: nothing was
	// withdrawn at all, so a live daemon that survived its drain still shows it.
	if msgs := f.s.agent.Messages(); !transcriptContains(msgs, "the prompt a restart interrupted") {
		t.Errorf("a drain deleted the prompt from the live transcript: %+v", msgs)
	}
	if msgs := f.resumeCold(t); !transcriptContains(msgs, "the prompt a restart interrupted") {
		t.Errorf("a drain deleted the prompt from the file: %+v", msgs)
	}
}

// A turn that was answered is not a withdrawal candidate, and the reload has to
// agree — this is the case where a broken amend index would eat somebody's real
// conversation rather than a typo.
func TestAnAnsweredPromptSurvivesAReload(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	s.agent.SetClientAndModel(&answeringFakeClient{}, "fake-model")

	if err := s.prompt("keep me", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never settled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = w.Close()

	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	if _, err := w2.ResumeSession(context.Background(), info.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if msgs := w2.live(info.ID).agent.Messages(); !transcriptContains(msgs, "keep me") {
		t.Errorf("an answered prompt vanished across a reload: %+v", msgs)
	}
}

// answeringFakeClient completes a turn normally.
type answeringFakeClient struct{}

func (c *answeringFakeClient) Name() string { return "answering-fake" }

func (c *answeringFakeClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}
