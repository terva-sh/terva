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

// newCarrierRepo makes a git repo for the carrier to lease from, and points
// TERVA_HOME at a scratch dir so the lease's state lands nowhere real.
func newCarrierRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
	} {
		if _, err := gitProbe(context.Background(), repo, args...); err != nil {
			t.Skipf("git %v unavailable: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitProbe(context.Background(), repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitProbe(context.Background(), repo, "commit", "-q", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestCarrierSwarmWorktreeLeasesDirectly: the stage-1 fold-in's payoff — the
// carrier leases from the in-tree engine with no extension anywhere: a fresh
// daemon with zero sessions can isolate a swarm sub-agent. Release frees the
// claim (the worktree survives for review/merge — release, never remove).
func TestCarrierSwarmWorktreeLeasesDirectly(t *testing.T) {
	repo := newCarrierRepo(t)
	w := &Workspace{cwd: repo, sessions: map[string]*wsSession{}}

	lease, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.Dir == "" {
		t.Fatal("lease has no directory")
	}
	if _, err := os.Stat(lease.Dir); err != nil {
		t.Fatalf("leased worktree missing on disk: %v", err)
	}
	if !strings.HasPrefix(lease.Dir, filepath.Join(os.Getenv("TERVA_HOME"), "worktrees")) {
		t.Errorf("lease should live under $TERVA_HOME/worktrees, got %s", lease.Dir)
	}
	if lease.Release == nil {
		t.Fatal("lease has no release hook")
	}
	lease.Release()

	// Released means available: a second agent can claim the same worktree.
	lease2, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if lease2.Dir != lease.Dir {
		t.Errorf("released worktree should be reused: %s vs %s", lease2.Dir, lease.Dir)
	}
}

// TestCarrierSwarmWorktreeFailsOutsideRepo: worktree isolation is opt-in and
// explicit, so a cwd with no git repo must fail the spawn LOUDLY rather than
// silently dropping back to the shared host tree.
func TestCarrierSwarmWorktreeFailsOutsideRepo(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{cwd: testsupport.TempDir(t), sessions: map[string]*wsSession{}}

	_, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err == nil {
		t.Fatal("acquireSwarmWorktree should fail outside a git repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error should say why: %v", err)
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
