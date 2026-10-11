package workspace

import (
	"path/filepath"
	"slices"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

func TestPermissionsWorkspaceRoots(t *testing.T) {
	primary, peer, scratch := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	sb := tools.NewSandbox(primary)
	if err := sb.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	sb.AddWritableRoot(scratch)
	sb.Lock()
	// AddRoot stores the canonical path: /private/var on macOS, the long name
	// for a Windows 8.3 temp path. The primary root keeps the form it was given.
	peer, err := filepath.EvalSymlinks(peer)
	if err != nil {
		t.Fatal(err)
	}
	s := &wsSession{cwd: primary, ws: &Workspace{sandbox: sb}}
	v := s.permissionsView()
	if !slices.Equal(v.WritableRoots, []string{primary, peer}) {
		t.Fatalf("permissions roots include the wrong grants: %v", v.WritableRoots)
	}
	v.WritableRoots[1] = scratch
	if !slices.Equal(s.permissionsView().WritableRoots, []string{primary, peer}) {
		t.Fatal("permissions view changed sandbox authority")
	}
	sb.Unlock()
	if got := s.permissionsView().WritableRoots; len(got) != 0 {
		t.Fatal("unjailed permissions view reports a bounded write set", got)
	}
}
