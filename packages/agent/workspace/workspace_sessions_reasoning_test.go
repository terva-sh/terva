package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func sessionRow(t *testing.T, rows []ctrlproto.SessionInfo, id string) ctrlproto.SessionInfo {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("session %s not in the list of %d", id, len(rows))
	return ctrlproto.SessionInfo{}
}

// The sessions LIST has to carry the session's own thinking level, because a
// client re-lists on every reconnect and on every sessions_changed event, and
// it replaces its whole array from the answer.
//
// When the list omitted the level, the client did not fall back to "unknown" —
// it fell back to the MODEL'S DEFAULT and rendered that as though it were the
// session's setting. So a mobile client that reconnected showed a level the
// session was not on, and the wrongness was invisible: a plausible level, in
// the right place, that the user had never chosen.
func TestSessionsListCarriesTheLiveThinkingLevel(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetSessionReasoning(ctx, info.ID, "high"); err != nil {
		t.Fatal(err)
	}

	rows, err := w.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRow(t, rows, info.ID).Reasoning; got != "high" {
		t.Errorf("live session row reasoning = %q, want %q", got, "high")
	}

	// "" is a real answer, not a missing one: it means the session follows the
	// global. Clearing has to come back as clearing, or the level would stick
	// in a client that only ever sees the list.
	if err := w.SetSessionReasoning(ctx, info.ID, ""); err != nil {
		t.Fatal(err)
	}
	rows, err = w.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRow(t, rows, info.ID).Reasoning; got != "" {
		t.Errorf("after clearing, row reasoning = %q, want empty", got)
	}
}

// The same question asked of a session the daemon has forgotten. A reconnect
// after a daemon restart lists from the disk scan, so the level has to survive
// in meta and reach the row without waking the session.
func TestColdSessionRowCarriesTheThinkingLevel(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	w1, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := w1.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w1.SetSessionReasoning(ctx, info.ID, "low"); err != nil {
		t.Fatal(err)
	}
	// An empty session is pruned on Close, so give it a row to survive on.
	if err := w1.live(info.ID).sess.AppendMessage(swipeMsg(provider.RoleAssistant, "hi")); err != nil {
		t.Fatal(err)
	}
	w1.Close()

	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	rows, err := w2.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := sessionRow(t, rows, info.ID)
	if row.Live {
		t.Fatal("session is materialized — this test must exercise the disk-scan path")
	}
	if row.Reasoning != "low" {
		t.Errorf("cold session row reasoning = %q, want %q", row.Reasoning, "low")
	}
}
