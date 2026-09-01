package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Renaming a live session and then doing anything that writes a meta row used
// to lose the rename: the row landed on disk, the session's own next write sat
// on top of it, and the title reverted. A client re-lists after a rename, so
// the reverted title is what the user saw a second later.
//
// The list is the assertion, not the live session's field — the field stayed
// correct the whole time, which is exactly why this read as a redraw bug.
func TestARenameSurvivesTheSessionsNextMetaWrite(t *testing.T) {
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
	if err := w.live(info.ID).sess.AppendMessage(swipeMsg(provider.RoleAssistant, "hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.RenameSession(ctx, info.ID, "My Chosen Name"); err != nil {
		t.Fatal(err)
	}
	if got := sessionRow(t, mustList(t, w, ctx), info.ID).Title; got != "My Chosen Name" {
		t.Fatalf("rename did not take at all: %q", got)
	}

	// Any meta write by the live session. Changing the thinking level is the
	// shortest one; a model switch or a persona change goes down the same path.
	if err := w.SetSessionReasoning(ctx, info.ID, "high"); err != nil {
		t.Fatal(err)
	}
	if got := sessionRow(t, mustList(t, w, ctx), info.ID).Title; got != "My Chosen Name" {
		t.Errorf("title reverted to %q after a meta write", got)
	}
}

func mustList(t *testing.T, w *Workspace, ctx context.Context) []ctrlproto.SessionInfo {
	t.Helper()
	rows, err := w.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
