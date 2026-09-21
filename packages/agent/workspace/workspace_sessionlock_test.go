package workspace

import (
	"context"
	"os"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func lockWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return w, cwd
}

// TestALiveWorkspaceSessionHoldsItsLock is the property the whole feature rests
// on for the daemon: while the panel has a session materialized, a second terva
// over the same home and directory cannot open it.
func TestALiveWorkspaceSessionHoldsItsLock(t *testing.T) {
	w, _ := lockWorkspace(t)
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !core.SessionIsLocked(info.Path) {
		t.Fatal("a live workspace session does not hold its lock")
	}
	claim, ok := core.SessionLockClaim(info.Path)
	if !ok || claim.Reason == "" {
		t.Fatalf("the claim must say why the session is held: %+v", claim)
	}
}

// TestDeletingASessionTakesItsClaimRecord: the claim is not a sidecar, so it is
// not in SessionSidecarPaths and has to be removed deliberately. Left behind, it
// would refuse a future session that landed on the same id.
func TestDeletingASessionTakesItsClaimRecord(t *testing.T) {
	w, _ := lockWorkspace(t)
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := info.Path
	if err := w.DeleteSession(context.Background(), info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, p := range core.SessionLockArtifactPaths(path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the delete", p)
		}
	}
}

// TestArchivingOurOwnLiveSessionStillWorks: the workspace closes its handle
// before archiving, so the guard added to core.ArchiveSession must never refuse
// the daemon that asked for it.
func TestArchivingOurOwnLiveSessionStillWorks(t *testing.T) {
	w, _ := lockWorkspace(t)
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}
	if err := s.sess.AppendMessage(provider.Message{
		Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ArchiveSession(context.Background(), info.ID); err != nil {
		t.Fatalf("archiving our own live session must work: %v", err)
	}
	for _, p := range core.SessionLockArtifactPaths(info.Path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the archive", p)
		}
	}
}
