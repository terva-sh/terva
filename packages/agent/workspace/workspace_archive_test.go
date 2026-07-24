package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// archiveWorkspace is a workspace rooted at a scratch home and cwd, with no live
// sessions — enough for the archive group, which is all file movement.
func archiveWorkspace(t *testing.T) *Workspace {
	t.Helper()
	return &Workspace{
		ctx:      context.Background(),
		diag:     func(string) {},
		sessions: map[string]*wsSession{},
		root:     testsupport.TempDir(t),
		cwd:      "/proj/archive-test",
	}
}

func seedTranscript(t *testing.T, w *Workspace, id, title string) string {
	t.Helper()
	dir := core.SessionsDir(w.root, w.cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"meta","meta":{"id":"` + id + `","title":"` + title + `","model":"claude-sonnet-4-5","provider":"anthropic"}}` + "\n" +
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"first thing said"}]}}` + "\n"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The round trip through the workspace, which is what every frontend actually
// calls: archive removes it from the session list, the archive lists it with a
// usable description, restore puts it back.
func TestWorkspaceArchiveListRestore(t *testing.T) {
	w := archiveWorkspace(t)
	ctx := context.Background()
	seedTranscript(t, w, "20260101-120000-aaaaaaaa", "kept")
	seedTranscript(t, w, "20260101-130000-bbbbbbbb", "to archive")

	info, err := w.ArchiveSession(ctx, "20260101-130000-bbbbbbbb")
	if err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if info.ID != "20260101-130000-bbbbbbbb" || info.Title != "to archive" {
		t.Errorf("archive result = %+v, want the id and title of the archived session", info)
	}
	if info.Preview != "first thing said" {
		t.Errorf("preview = %q, want the first user message so an untitled row is still legible", info.Preview)
	}

	if got := len(core.ListSessions(w.root, w.cwd)); got != 1 {
		t.Fatalf("%d sessions listed after archiving, want 1", got)
	}

	list, err := w.ArchivedSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "20260101-130000-bbbbbbbb" {
		t.Fatalf("archived list = %+v, want the one archived session", list)
	}
	if list[0].ArchivedAt == "" {
		t.Error("no archived-at stamp on the wire row")
	}

	restored, err := w.RestoreSession(ctx, ctrlproto.RestoreSessionParams{ID: "20260101-130000-bbbbbbbb"})
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if restored.Title != "to archive" {
		t.Errorf("restored info title = %q, want the session's title", restored.Title)
	}
	if got := len(core.ListSessions(w.root, w.cwd)); got != 2 {
		t.Errorf("%d sessions listed after restore, want 2", got)
	}
	if got, _ := w.ArchivedSessions(ctx); len(got) != 0 {
		t.Errorf("archive still holds %d after restore", len(got))
	}
}

// Archiving must close the live handle first. A writer left open on a file that
// has moved out of the sessions directory appends turns nothing will ever read,
// and reports no error while doing it.
func TestArchiveClosesTheLiveSession(t *testing.T) {
	w := archiveWorkspace(t)
	seedTranscript(t, w, "20260101-120000-cccccccc", "live")

	closed := false
	w.sessions["20260101-120000-cccccccc"] = &wsSession{id: "20260101-120000-cccccccc", hub: newWSHub(), stopExt: func() { closed = true }}

	if _, err := w.ArchiveSession(context.Background(), "20260101-120000-cccccccc"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if !closed {
		t.Error("the live session was not closed before its transcript moved")
	}
	if _, still := w.sessions["20260101-120000-cccccccc"]; still {
		t.Error("the archived session is still in the live set")
	}
}

// A client-supplied id is a bare filename stem, never a path. Without the guard
// the archive verbs would move arbitrary .jsonl files out of, or into, the
// sessions directory.
func TestArchiveVerbsRejectPathTraversal(t *testing.T) {
	w := archiveWorkspace(t)
	ctx := context.Background()
	for _, bad := range []string{"../escape", "sub/dir", "..", "", "#workspace"} {
		if _, err := w.ArchiveSession(ctx, bad); err == nil {
			t.Errorf("ArchiveSession(%q) was accepted", bad)
		}
		if _, err := w.RestoreSession(ctx, ctrlproto.RestoreSessionParams{ID: bad}); err == nil {
			t.Errorf("RestoreSession(%q) was accepted", bad)
		}
	}

	// The refusals above are not enough on their own: an id that escapes the
	// sessions directory usually names nothing, so "no such session" and "the
	// guard stopped it" are indistinguishable. Put a real file where the
	// traversal would land and prove it is still there.
	outside := filepath.Join(core.SessionsDir(w.root, w.cwd), "..", "someone-elses.jsonl")
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(`{"type":"meta","meta":{"id":"x"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ArchiveSession(ctx, "../someone-elses"); err == nil {
		t.Error("a traversing id was accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the sessions directory was moved by an archive: %v", err)
	}
}

// Restoring onto a live id is refused with a message that says the live session
// was left alone — the one thing the operator needs to know.
func TestWorkspaceRestoreRefusesLiveCollision(t *testing.T) {
	w := archiveWorkspace(t)
	ctx := context.Background()
	id := "20260101-120000-dddddddd"
	seedTranscript(t, w, id, "original")
	if _, err := w.ArchiveSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, w, id, "live now")

	_, err := w.RestoreSession(ctx, ctrlproto.RestoreSessionParams{ID: id})
	if err == nil {
		t.Fatal("restore clobbered a live session")
	}
	if !strings.Contains(err.Error(), "left untouched") {
		t.Errorf("error = %v, want it to say the live session survived", err)
	}
}

// Archiving a session that does not exist is ErrNoSession, not a success or an
// internal error — the frontends key their "gone already" handling on it.
func TestArchiveUnknownSessionIsNoSession(t *testing.T) {
	w := archiveWorkspace(t)
	_, err := w.ArchiveSession(context.Background(), "20260101-120000-99999999")
	if err == nil {
		t.Fatal("archiving a nonexistent session reported success")
	}
	if !errors.Is(err, ctrlproto.ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}
