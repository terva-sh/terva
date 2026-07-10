package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestLiveResolvesDefaultSession pins the ” semantics of the
// Cancel/Approve/Answer/Queue/SetQueue family: an empty session id
// names the workspace default (exactly like resolve), while lookups
// stay strictly no-create — a raati_convene approval smoke found
// approvals with "" silently dropped because these methods looked the
// id up verbatim when every sibling method resolved it.
func TestLiveResolvesDefaultSession(t *testing.T) {
	root, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	sess, err := core.NewSession(root, cwd, "prov", "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately not closed until the test ends: closing an empty
	// fresh session prunes its file, and the default-session lookup
	// reads the disk.
	defer sess.Close()

	id := build.SessionIDFromPath(sess.Path)
	stub := &wsSession{}
	w := &Workspace{root: root, cwd: cwd, sessions: map[string]*wsSession{id: stub}}

	if got := w.live(""); got != stub {
		t.Errorf(`live("") = %v, want the live default session`, got)
	}
	if got := w.live(id); got != stub {
		t.Errorf("live(%q) missed the live session", id)
	}
	if got := w.live("20991231-000000-deadbeef"); got != nil {
		t.Errorf("live(unknown) = %v, want nil", got)
	}

	// The default session exists on disk but is NOT live: still nil —
	// this family never materializes a session (there could be no
	// parked confirmation or queue on it anyway).
	idle := &Workspace{root: root, cwd: cwd, sessions: map[string]*wsSession{}}
	if got := idle.live(""); got != nil {
		t.Errorf(`live("") on a not-live default = %v, want nil (never builds)`, got)
	}

	// No sessions on disk at all: nil, and nothing gets created.
	empty := &Workspace{root: testsupport.TempDir(t), cwd: cwd, sessions: map[string]*wsSession{}}
	if got := empty.live(""); got != nil {
		t.Errorf(`live("") with no sessions = %v, want nil`, got)
	}
}
