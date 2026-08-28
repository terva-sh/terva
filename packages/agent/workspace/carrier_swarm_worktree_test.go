package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/worktree"
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
// daemon with zero sessions can isolate a swarm sub-agent.
//
// Release RECLAIMS a worktree that holds nothing. This test used to assert the
// opposite ("release, never remove") and then check that a second acquire
// reused the same directory. That check survives the behaviour change while
// meaning something entirely different — the name is derived from the agent id
// and task, so the path is identical whether the worktree was reused or
// deleted and made again. Asserting the directory is GONE is what tells the two
// apart.
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
	// Derived from the constructor, not re-spelled: an assertion that hardcodes
	// the root keeps passing when production moves off it.
	wantRoot, _ := worktree.HostRoots(os.Getenv("TERVA_HOME"))
	if !strings.HasPrefix(lease.Dir, wantRoot) {
		t.Errorf("lease should live under %s, got %s", wantRoot, lease.Dir)
	}
	if lease.Release == nil {
		t.Fatal("lease has no release hook")
	}
	lease.Release()

	// Nothing was written in it, so nothing is worth keeping.
	if _, err := os.Stat(lease.Dir); !os.IsNotExist(err) {
		t.Errorf("a clean worktree survived its agent at %s (stat err: %v)", lease.Dir, err)
	}

	// And the next agent still gets a working directory at that path — rebuilt,
	// not resurrected.
	lease2, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if lease2.Dir != lease.Dir {
		t.Errorf("the derived path should be stable: %s vs %s", lease2.Dir, lease.Dir)
	}
	if _, err := os.Stat(lease2.Dir); err != nil {
		t.Errorf("re-acquired worktree missing on disk: %v", err)
	}
}

// TestCarrierKeepsAWorktreeThatHoldsWork is the other half, and the one that
// matters if the policy is ever wrong: a sub-agent that edited a file must find
// its work still there after it exits. Driven through the real lease hook,
// because that is where the reclaim/keep decision is wired — a unit test of the
// engine would pass with the hook calling the wrong thing.
func TestCarrierKeepsAWorktreeThatHoldsWork(t *testing.T) {
	repo := newCarrierRepo(t)
	w := &Workspace{cwd: repo, sessions: map[string]*wsSession{}}

	lease, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "busy", Task: "write something"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	scratch := filepath.Join(lease.Dir, "unsaved.txt")
	if err := os.WriteFile(scratch, []byte("work nobody committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lease.Release()

	if _, err := os.Stat(lease.Dir); err != nil {
		t.Fatalf("worktree with uncommitted work was reclaimed: %v", err)
	}
	got, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("the uncommitted file did not survive: %v", err)
	}
	if string(got) != "work nobody committed\n" {
		t.Errorf("file contents changed: %q", got)
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
