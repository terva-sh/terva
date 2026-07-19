package worktree

import (
	"terva.sh/terva/packages/testsupport"

	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- test harness ----------------------------------------------------------

func runT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// newRepo creates a fresh git repo on branch main with one commit and returns
// its path plus a separate extension data dir.
func newRepo(t *testing.T) (repoDir, dataDir string) {
	t.Helper()
	repoDir = testsupport.TempDir(t)
	dataDir = testsupport.TempDir(t)
	if _, err := runGit(repoDir, "init", "-q", "-b", "main"); err != nil {
		t.Skipf("git init -b unsupported: %v", err)
	}
	runT(t, repoDir, "config", "user.email", "t@example.com")
	runT(t, repoDir, "config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, repoDir, "add", "-A")
	runT(t, repoDir, "commit", "-q", "-m", "init")
	return repoDir, dataDir
}

// fixedManager returns a Manager with a frozen clock and a controllable
// pid-liveness probe (alive=true means every claim's pid reads as live).
func fixedManager(alive bool) *Manager {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	return &Manager{
		now:      func() time.Time { return now },
		pidAlive: func(int) bool { return alive },
		selfPID:  4242,
		ttl:      defaultClaimTTL,
	}
}

func env(repoDir, dataDir, session string) Env {
	return Env{Root: dataDir, CWD: repoDir, SessionID: session}
}

// --- tests -----------------------------------------------------------------

func TestCreateAndList(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	wantBase := headCommit(repoDir)

	res, err := m.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "feat-auth", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "claimed" || res.ClaimedBy != "self" || res.Reused {
		t.Errorf("create result: %+v", res)
	}
	if res.Branch != "wt/feat-auth" || res.BaseRef != "main" || res.BaseCommit != wantBase {
		t.Errorf("create base/branch: %+v (want base %s)", res, wantBase)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("worktree dir missing: %v", err)
	}

	lst, err := m.List(env(repoDir, dataDir, "sess-1"), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lst.Worktrees) != 1 {
		t.Fatalf("want 1 worktree, got %d", len(lst.Worktrees))
	}
	it := lst.Worktrees[0]
	if it.Name != "feat-auth" || it.Status != "claimed" || it.ClaimedBy == nil || *it.ClaimedBy != "self" {
		t.Errorf("list item: %+v", it)
	}
	if it.BaseCommit != wantBase || it.Dirty {
		t.Errorf("list base/dirty: %+v", it)
	}
	if lst.RepoKey == "" {
		t.Error("repo_key should be set")
	}
}

func TestCreateReuseWhenAvailable(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	// sess-1 creates and claims.
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "shared", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// A new instance where sess-1's pid is dead => its claim is stale/available.
	m2 := fixedManager(false)
	res, err := m2.Create(env(repoDir, dataDir, "sess-2"), CreateArgs{Name: "shared", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reused {
		t.Errorf("expected reused=true, got %+v", res)
	}
	if res.ClaimedBy != "self" {
		t.Errorf("reuse should claim for caller: %+v", res)
	}
	// Now sess-2 holds it; from sess-2 it lists as claimed-by-self.
	lst, _ := m2.List(env(repoDir, dataDir, "sess-2"), ListFilter{})
	if got := lst.Worktrees[0]; got.Status != "claimed" || got.ClaimedBy == nil || *got.ClaimedBy != "self" {
		t.Errorf("after reuse: %+v", got)
	}
}

func TestCreateRefusedWhenClaimedByOther(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "busy", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// pid alive + different session => live claim by another => refuse.
	_, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-2"), CreateArgs{Name: "busy", ReuseIfAvailable: true})
	if err == nil || !strings.Contains(err.Error(), "claimed by") {
		t.Errorf("expected claimed-by-other error, got %v", err)
	}
}

func TestCreateExistingAvailableButReuseDisabled(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "x", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	_, err := fixedManager(false).Create(env(repoDir, dataDir, "sess-2"), CreateArgs{Name: "x", ReuseIfAvailable: false})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected already-exists error, got %v", err)
	}
}

func TestListStatusStaleAndDirtyAndCWD(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	res, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "w", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	// From another session with the owner's pid alive => claimed by sess-1, fresh.
	live, _ := fixedManager(true).List(env(repoDir, dataDir, "sess-2"), ListFilter{})
	it := live.Worktrees[0]
	if it.Status != "claimed" || it.ClaimedBy == nil || *it.ClaimedBy != "sess-1" || it.StaleReason != "" {
		t.Errorf("live other-claim: %+v stale_reason=%q", it, it.StaleReason)
	}

	// Same but the owner's pid is gone => available + stale, claimed_by null.
	stale, _ := fixedManager(false).List(env(repoDir, dataDir, "sess-2"), ListFilter{})
	st := stale.Worktrees[0]
	if st.Status != "available" || st.ClaimedBy != nil || st.StaleReason == "" {
		t.Errorf("stale claim: %+v", st)
	}

	// cwd_worktree: null from the repo root, the name from inside the worktree.
	if live.CWDWorktree != nil {
		t.Errorf("cwd_worktree should be null at repo root, got %v", *live.CWDWorktree)
	}
	inside, _ := fixedManager(true).List(env(res.Path, dataDir, "sess-1"), ListFilter{})
	if inside.CWDWorktree == nil || *inside.CWDWorktree != "w" {
		t.Errorf("cwd_worktree from inside worktree: %+v", inside.CWDWorktree)
	}

	// dirty: an uncommitted change shows up.
	if err := os.WriteFile(filepath.Join(res.Path, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := fixedManager(true).List(env(repoDir, dataDir, "sess-1"), ListFilter{})
	if !d.Worktrees[0].Dirty {
		t.Error("expected dirty=true after writing an untracked file")
	}
}

func TestRemoveRefusesDirtyAndUnmergedThenForce(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "r", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	// Dirty (uncommitted) => refuse without force.
	if err := os.WriteFile(filepath.Join(res.Path, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(env(repoDir, dataDir, "sess-1"), RemoveArgs{Name: "r"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("expected uncommitted refusal, got %v", err)
	}

	// Commit it => now clean but unmerged (commits beyond base, no upstream).
	runT(t, res.Path, "add", "-A")
	runT(t, res.Path, "commit", "-q", "-m", "wip")
	if _, err := m.Remove(env(repoDir, dataDir, "sess-1"), RemoveArgs{Name: "r"}); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Errorf("expected unmerged refusal, got %v", err)
	}

	// Force removes it, prunes, drops the registry entry; branch is left.
	rm, err := m.Remove(env(repoDir, dataDir, "sess-1"), RemoveArgs{Name: "r", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rm.Removed || rm.BranchDeleted {
		t.Errorf("force remove: %+v", rm)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone: %v", err)
	}
	lst, _ := m.List(env(repoDir, dataDir, "sess-1"), ListFilter{})
	if len(lst.Worktrees) != 0 {
		t.Errorf("registry entry should be dropped, got %+v", lst.Worktrees)
	}
	if branches := runT(t, repoDir, "branch", "--list", "wt/r"); !strings.Contains(branches, "wt/r") {
		t.Errorf("branch should be left by default, got %q", branches)
	}
}

func TestRemoveDeleteBranch(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "gone", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	rm, err := m.Remove(env(repoDir, dataDir, "sess-1"), RemoveArgs{Name: "gone", DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rm.Removed || !rm.BranchDeleted {
		t.Errorf("delete-branch remove: %+v", rm)
	}
	if b := runT(t, repoDir, "branch", "--list", "wt/gone"); strings.TrimSpace(b) != "" {
		t.Errorf("branch should be deleted, got %q", b)
	}
}

func TestReconcileDroppedAndUnmanaged(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "managed", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	// Remove the worktree out-of-band (raw git) => list reconciles it away.
	runT(t, repoDir, "worktree", "remove", "--force", res.Path)
	lst, _ := m.List(env(repoDir, dataDir, "sess-1"), ListFilter{})
	for _, it := range lst.Worktrees {
		if it.Name == "managed" {
			t.Errorf("dropped worktree should disappear from list, got %+v", it)
		}
	}

	// Add a worktree under our dir out-of-band => shows up as unmanaged.
	ext := filepath.Join(dataDir, lst.RepoKey, "worktrees", "rogue")
	runT(t, repoDir, "worktree", "add", "--quiet", ext, "-b", "rogue-branch")
	lst2, _ := m.List(env(repoDir, dataDir, "sess-1"), ListFilter{})
	var found *ListItem
	for _, it := range lst2.Worktrees {
		if it.Name == "rogue" {
			found = it
		}
	}
	if found == nil || !found.Unmanaged {
		t.Errorf("externally-added worktree should be unmanaged, got %+v", lst2.Worktrees)
	}
}

func TestRepoKeyStableAcrossWorktrees(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "k", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	fromRoot, _ := resolveRepo(env(repoDir, dataDir, "sess-1"))
	fromWT, _ := resolveRepo(env(res.Path, dataDir, "sess-1"))
	if fromRoot.key != fromWT.key {
		t.Errorf("repo key must be stable across worktrees: %q vs %q", fromRoot.key, fromWT.key)
	}
}

func TestCreateWithExplicitBase(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	// second commit so HEAD != first; branch from the first commit explicitly.
	first := headCommit(repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "two.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, repoDir, "add", "-A")
	runT(t, repoDir, "commit", "-q", "-m", "second")

	res, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "old", Base: first, ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.BaseCommit != first || res.BaseRef != first {
		t.Errorf("explicit base: %+v (want %s)", res, first)
	}
	if res.HeadCommit != first {
		t.Errorf("head should be the base commit on a fresh worktree: %+v", res)
	}
}

func TestCreateBadBase(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	_, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "b", Base: "no-such-ref"})
	if err == nil || !strings.Contains(err.Error(), "not a valid ref") {
		t.Errorf("expected invalid-ref error, got %v", err)
	}
}

func TestNotAGitRepo(t *testing.T) {
	dir := testsupport.TempDir(t) // not a git repo
	_, err := fixedManager(true).List(env(dir, testsupport.TempDir(t), "sess-1"), ListFilter{})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("expected not-a-git-repo error, got %v", err)
	}
}

// guard ensures the test binary actually has git available; skip loudly if not.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		os.Stderr.WriteString("git not found on PATH; skipping worktree tests\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
