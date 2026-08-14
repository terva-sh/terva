package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
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
	s1, err := core.NewSession(tmp, tmp, "anthropic", "m1", "test")
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
	sess, err := core.NewSession(tmp, tmp, "anthropic", "m1", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s := &wsSession{
		id:    build.SessionIDFromPath(sess.Path),
		sess:  sess,
		agent: core.NewAgent(nil, "anthropic", "m1", core.Registry{}),
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
