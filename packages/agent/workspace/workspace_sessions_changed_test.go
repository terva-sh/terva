package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// assertSessionsChanged drains the workspace stream until it sees a
// sessions_changed (tolerating an unrelated event that may ride ahead of it),
// failing if none arrives.
func assertSessionsChanged(t *testing.T, ch <-chan ctrlproto.Event, when string) {
	t.Helper()
	for {
		ev, ok := recv(t, ch)
		if !ok {
			t.Fatalf("%s: no sessions_changed — a board would never re-list", when)
		}
		if ev.Type == ctrlproto.EventSessionsChanged {
			return
		}
	}
}

func infoByID(list []ctrlproto.SessionInfo, id string) (ctrlproto.SessionInfo, bool) {
	for _, s := range list {
		if s.ID == id {
			return s, true
		}
	}
	return ctrlproto.SessionInfo{}, false
}

// A rename and a delete each change the session set's shape, so both broadcast
// sessions_changed on the workspace address — the signal a board keys its tile
// add/prune off. Cold sessions on disk report neither Live nor Busy. Uses real
// session files and no live agent (no credentials needed).
func TestSessionsChangedOnRenameAndDelete(t *testing.T) {
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	ctx := context.Background()

	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}
	s1, err := session.NewSession(tmp, tmp, "anthropic", "m1", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = s1.AppendMessage(msg)
	_ = s1.Close()
	id := build.SessionIDFromPath(s1.Path)

	// A cold session (on disk, not materialized) is neither live nor busy.
	list, _ := w.Sessions(ctx)
	if info, ok := infoByID(list, id); !ok || info.Live || info.Busy {
		t.Fatalf("cold session must be present and neither Live nor Busy: %+v (ok=%v)", info, ok)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Subscribe(subCtx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := w.RenameSession(ctx, id, "renamed"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	assertSessionsChanged(t, ch, "after rename")

	if err := w.DeleteSession(ctx, id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	assertSessionsChanged(t, ch, "after delete")
}

// Creating a session adds it to the set (sessions_changed) and the new session
// is Live in the listing. Needs a real workspace/agent, built offline exactly
// like the carrier tests (TERVA_HOME + a throwaway key; no turn is run).
func TestSessionsChangedOnCreate(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	ctx := context.Background()

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Subscribe(subCtx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	assertSessionsChanged(t, ch, "after create")

	list, err := w.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if got, ok := infoByID(list, info.ID); !ok || !got.Live {
		t.Errorf("a just-created session must be Live in the listing: %+v (ok=%v)", got, ok)
	}
}

// info() describes a materialized session, so it is always Live; Busy tracks an
// in-flight turn (a running turn holds turnCancel).
func TestSessionInfoReportsLiveAndBusy(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "anthropic", "m1", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s := &wsSession{
		id:    build.SessionIDFromPath(sess.Path),
		sess:  sess,
		agent: coretest.NewAgent(nil, "anthropic", "m1", core.Registry{}),
		hub:   newWSHub(),
	}

	if info := s.info(); !info.Live || info.Busy {
		t.Fatalf("idle live session: want Live && !Busy, got Live=%v Busy=%v", info.Live, info.Busy)
	}

	_, s.turnCancel = context.WithCancelCause(context.Background())
	if info := s.info(); !info.Busy {
		t.Error("a session with an in-flight turn must report Busy")
	}
}

// A session that waits on a person says so in the listing, and the daemon
// pushes a re-list each time a request opens or closes. A board or palette that
// is not subscribed to the session hears neither request, so the list is the
// only place it can read the state.
func TestSessionInfoReportsWhatALiveSessionWaitsOn(t *testing.T) {
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	ctx := context.Background()

	sess, err := session.NewSession(tmp, tmp, "anthropic", "m1", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	live := newTestSession()
	live.id, live.ws, live.sess = build.SessionIDFromPath(sess.Path), w, sess
	live.agent = coretest.NewAgent(nil, "anthropic", "m1", core.Registry{})
	w.sessions[live.id] = live

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Subscribe(subCtx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waiting := func(when string) string {
		t.Helper()
		list, err := w.Sessions(ctx)
		if err != nil {
			t.Fatalf("%s: Sessions: %v", when, err)
		}
		row, ok := infoByID(list, live.id)
		if !ok {
			t.Fatalf("%s: the live session is missing from the list", when)
		}
		if got := live.info().Waiting; got != row.Waiting {
			t.Fatalf("%s: info() says %q and the list says %q", when, got, row.Waiting)
		}
		return row.Waiting
	}

	// The positive control: an idle session waits on nothing.
	if got := waiting("idle"); got != "" {
		t.Fatalf("an idle session reports Waiting=%q", got)
	}

	live.openAsk(ctrlproto.AskRequest{AskID: "a1", Question: "Which port?"})
	assertSessionsChanged(t, ch, "after a question opened")
	if got := waiting("a question open"); got != ctrlproto.WaitingQuestion {
		t.Fatalf("Waiting = %q with a question open, want %q", got, ctrlproto.WaitingQuestion)
	}

	// An approval outranks a question: it holds the turn on a tool call.
	live.openPermission(ctrlproto.PermissionRequest{CallID: "c1", Tool: "bash"})
	assertSessionsChanged(t, ch, "after an approval opened")
	if got := waiting("both open"); got != ctrlproto.WaitingApproval {
		t.Fatalf("Waiting = %q with both open, want %q", got, ctrlproto.WaitingApproval)
	}

	live.closePermission("c1", "approved")
	assertSessionsChanged(t, ch, "after the approval closed")
	if got := waiting("the question left"); got != ctrlproto.WaitingQuestion {
		t.Fatalf("Waiting = %q after the approval closed, want %q", got, ctrlproto.WaitingQuestion)
	}

	live.closeAsk("a1", "answered")
	assertSessionsChanged(t, ch, "after the question closed")
	if got := waiting("nothing open"); got != "" {
		t.Fatalf("Waiting = %q with nothing open", got)
	}
}
