package workspace

// Stage 2 of docs/proposals/shell-escape-context.md: the daemon accepts a
// client's shell-escape result and hands it to the agent.
//
// Driven through Workspace.ShellResult and a real session rather than by
// calling core's setter, because the wiring IS the stage — a test that armed
// the agent directly would pass with the verb unimplemented.

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

// wsCapturingClient answers every turn and keeps each request's ephemeral tail.
type wsCapturingClient struct {
	tails chan string
}

func (c *wsCapturingClient) Name() string { return "ws-capturing-fake" }

func (c *wsCapturingClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	select {
	case c.tails <- req.EphemeralContext:
	default:
	}
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

type shellFixture struct {
	w      *Workspace
	s      *wsSession
	id     string
	cwd    string
	client *wsCapturingClient
}

func newShellFixture(t *testing.T) *shellFixture {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}
	client := &wsCapturingClient{tails: make(chan string, 8)}
	s.agent.SetClientAndModel(client, "fake-model")
	// The shipped default is OFF. Said explicitly here, because a fixture that
	// forgot would assert against a setter that quietly does nothing.
	s.agent.SetShellResultContext(true)
	return &shellFixture{w: w, s: s, id: info.ID, cwd: cwd, client: client}
}

// promptAndTail runs one turn and returns the tail the request carried.
func (f *shellFixture) promptAndTail(t *testing.T, text string) string {
	t.Helper()
	if err := f.s.prompt(text, nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never settled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case tail := <-f.client.tails:
		return tail
	case <-time.After(2 * time.Second):
		t.Fatal("no request reached the provider")
	}
	return ""
}

// --- the case the stage exists for ------------------------------------------

func TestAShellResultOfferedOverTheWireReachesTheNextRequest(t *testing.T) {
	f := newShellFixture(t)

	if err := f.w.ShellResult(context.Background(), f.id, ctrlproto.ShellResultParams{
		Command: "git status",
		Output:  "3 files changed",
	}); err != nil {
		t.Fatalf("ShellResult: %v", err)
	}

	tail := f.promptAndTail(t, "what should I commit first?")
	for _, want := range []string{core.ShellResultTag, "git status", "3 files changed"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the tail is missing %q:\n%s", want, tail)
		}
	}
}

// The whole point of the tail is that it leaves no trace. A cold resume is the
// honest check: core removing it from the live transcript would look identical
// to it never having been written, and this codebase has been bitten by an
// in-memory change with no twin in the file replay.
func TestAShellResultIsNeverWrittenToTheSession(t *testing.T) {
	f := newShellFixture(t)

	if err := f.w.ShellResult(context.Background(), f.id, ctrlproto.ShellResultParams{
		Command: "cat secrets.env",
		Output:  "SUPER-SECRET-VALUE",
	}); err != nil {
		t.Fatalf("ShellResult: %v", err)
	}
	if tail := f.promptAndTail(t, "anything interesting?"); !strings.Contains(tail, "SUPER-SECRET-VALUE") {
		t.Fatal("the result never rode, so this proves nothing about what was stored")
	}

	// Cold resume: close the workspace and read the session back off disk.
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
		t.Fatal("the resumed session is not live")
	}
	for _, m := range s2.agent.Messages() {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, "SUPER-SECRET-VALUE") {
				t.Fatalf("shell output was persisted to the session file; it is supposed to be ephemeral:\n%s", tb.Text)
			}
		}
	}
}

// --- the disarm --------------------------------------------------------------

func TestAnEmptyCommandOverTheWireDisarms(t *testing.T) {
	f := newShellFixture(t)

	if err := f.w.ShellResult(context.Background(), f.id, ctrlproto.ShellResultParams{
		Command: "git status", Output: "3 files changed",
	}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	// A client that ran a command and then decided not to offer it.
	if err := f.w.ShellResult(context.Background(), f.id, ctrlproto.ShellResultParams{
		Command: "", Output: "ignored",
	}); err != nil {
		t.Fatalf("disarm must not be an error, a client withdrawing an offer is ordinary: %v", err)
	}

	tail := f.promptAndTail(t, "hello")
	if strings.Contains(tail, "3 files changed") || strings.Contains(tail, "ignored") {
		t.Errorf("the disarm left something armed:\n%s", tail)
	}
}

func TestAShellResultForAnUnknownSessionIsAnError(t *testing.T) {
	f := newShellFixture(t)
	err := f.w.ShellResult(context.Background(), "no-such-session", ctrlproto.ShellResultParams{
		Command: "git status", Output: "x",
	})
	if err == nil {
		t.Error("arming an unknown session succeeded")
	}
}

// The shipped default is the privacy-relevant part of this feature, so it is
// pinned where a user would actually read it: the settings surface every client
// renders. A default flipped by accident is not a behaviour change anyone would
// notice in a diff — it is terminal output starting to reach a provider.
func TestTheSettingsSurfaceOffersTheFeatureOff(t *testing.T) {
	// Deliberately NOT newShellFixture, which turns the feature on: this asks
	// what a user is offered, not what a test arranged.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, it := range w.live(info.ID).settingsView().Items {
		if it.Key != "shell_result_context" {
			continue
		}
		if it.Value != "false" {
			t.Errorf("the feature is offered ON (%q); a fresh install would send terminal output to a provider", it.Value)
		}
		// The description has to say where the output GOES, not only what the
		// feature does — that is the whole basis for the default.
		if !strings.Contains(it.Description, "provider") {
			t.Errorf("the description does not say the output leaves the machine:\n%s", it.Description)
		}
		return
	}
	t.Error("the feature is absent from the settings surface, so nobody can turn it on")
}
