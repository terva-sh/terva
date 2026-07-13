package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestCarrierSwarmWorktreeAcquirerNoExt: worktree isolation is opt-in and
// explicit, so when no live session provides worktree_create the acquirer must
// fail the spawn LOUDLY rather than silently dropping back to the shared host
// tree (which would defeat the point of --swarm-worktrees).
func TestCarrierSwarmWorktreeAcquirerNoExt(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}

	if mgr := w.worktreeExtManager(); mgr != nil {
		t.Fatalf("worktreeExtManager = %v with no sessions, want nil", mgr)
	}

	_, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err == nil {
		t.Fatal("acquireSwarmWorktree should fail when no worktree extension is available")
	}
	if !strings.Contains(err.Error(), "worktree_create") {
		t.Errorf("error should name the missing tool: %v", err)
	}
}

// TestWorktreeTrustVerdict pins the swarm-worktree trust core that closes the
// silent-degradation hole: the swarm_spawn gate only sees the host cwd, so a
// leased worktree outside the trusted path boots restricted while the gate
// passes. The verdict must reflect the store verbatim — trusting a worktree only
// by an explicit entry or the store's own --parent rule, never by inheriting the
// host's verdict — and it must name WHY (the retro H5·ux legibility goal), so an
// inherited --parent grant reads as inherited rather than silent.
func TestWorktreeTrustVerdict(t *testing.T) {
	parent := testsupport.TempDir(t) // trusted with --parent (covers descendants)
	repo := testsupport.TempDir(t)   // trusted as itself only (non-parent)

	// Real dirs so CanonicalTrustPath's EvalSymlinks resolves the worktree paths
	// through the same symlinks as their trusted roots (macOS /var -> /private/var).
	underParent := filepath.Join(parent, "wt-agent-a")
	underRepo := filepath.Join(repo, "wt-agent-b")
	for _, d := range []string{underParent, underRepo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outside := testsupport.TempDir(t) // absent from the store entirely

	store := config.TrustStore{Version: 1, Trusted: []config.TrustEntry{
		{Path: parent, Real: config.CanonicalTrustPath(parent), Parent: true},
		{Path: repo, Real: config.CanonicalTrustPath(repo), Parent: false},
	}}

	// Worktree under a --parent entry: trusted, and the reason names the inherit.
	if ok, reason := worktreeTrustVerdict(store, underParent); !ok {
		t.Errorf("worktree under a --parent entry must be trusted, got restricted (%q)", reason)
	} else if !strings.Contains(reason, "--parent") {
		t.Errorf("inherited grant must say so; reason = %q", reason)
	}
	// Exact non-parent entry: trusted as an explicit grant, not an inherit.
	if ok, reason := worktreeTrustVerdict(store, repo); !ok {
		t.Errorf("the trusted repo dir itself must be trusted, got restricted (%q)", reason)
	} else if strings.Contains(reason, "--parent") {
		t.Errorf("an exact entry is not an inherit; reason = %q", reason)
	}
	// Worktree under a NON-parent entry: not extended to descendants — restricted.
	if ok, _ := worktreeTrustVerdict(store, underRepo); ok {
		t.Error("worktree under a non-parent trust entry must be restricted (no auto-inherit)")
	}
	// Worktree absent from the store: restricted.
	if ok, _ := worktreeTrustVerdict(store, outside); ok {
		t.Error("worktree absent from the trust store must be restricted")
	}
	// Empty path is restricted, never a spurious trust.
	if ok, _ := worktreeTrustVerdict(store, ""); ok {
		t.Error("empty worktree path must be restricted")
	}
}
