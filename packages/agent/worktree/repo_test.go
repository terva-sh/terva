package worktree

import (
	"terva.sh/terva/packages/testsupport"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initChildRepo git-inits parent/name and returns its path. A freshly-init'd
// repo (no commits) is enough: --git-common-dir resolves without a commit.
func initChildRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(dir, "init", "-q", "-b", "main"); err != nil {
		t.Skipf("git init unsupported: %v", err)
	}
	return dir
}

// envRoot is env() with an explicit repo_root override (real cwd kept separate).
func envRoot(cwd, repoRoot, dataDir, session string) Env {
	return Env{Root: dataDir, CWD: cwd, SessionID: session, RepoRoot: repoRoot}
}

// TestRepoRootOverrideResolvesIndependentOfCwd: with cwd a non-git dir and an
// explicit repo_root pointing at a real repo, the tools resolve the repo and
// work — the escape hatch the discoverability proposal asks for.
func TestRepoRootOverrideResolvesIndependentOfCwd(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	scratch := testsupport.TempDir(t) // cwd: NOT a git repo

	m := fixedManager(true)

	// A create targeting the repo via repo_root must succeed from a non-repo cwd.
	if _, err := m.Create(envRoot(scratch, repoDir, dataDir, "s1"), CreateArgs{Name: "feat"}); err != nil {
		t.Fatalf("Create with repo_root from non-repo cwd: %v", err)
	}

	// And list it back through the same override.
	res, err := m.List(envRoot(scratch, repoDir, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatalf("List with repo_root from non-repo cwd: %v", err)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Name != "feat" {
		t.Fatalf("expected the feat worktree via repo_root, got %+v", res.Worktrees)
	}

	// Sanity: the same override yields the same repo key as resolving the repo
	// directly from its own cwd (it targets the same git common dir).
	direct, err := m.List(env(repoDir, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatalf("List from repo cwd: %v", err)
	}
	if res.RepoKey != direct.RepoKey {
		t.Errorf("repo_root keyed a different repo: %q vs %q", res.RepoKey, direct.RepoKey)
	}
}

// TestRepoRootRelativeToCwd: a relative repo_root resolves against cwd.
func TestRepoRootRelativeToCwd(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	parent := filepath.Dir(repoDir)
	rel := filepath.Base(repoDir) // repoDir == parent/rel

	res, err := fixedManager(true).List(envRoot(parent, rel, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatalf("List with relative repo_root %q from %q: %v", rel, parent, err)
	}
	if res.RepoKey == "" {
		t.Error("expected a resolved repo key from a relative repo_root")
	}
}

// TestRepoRootCWDWorktreeReflectsRealCwd: cwd_worktree is keyed on the real cwd,
// not the repo_root override — so operating on a repo the agent isn't inside
// reports cwd_worktree=null rather than implying it's "in" a worktree.
func TestRepoRootCWDWorktreeReflectsRealCwd(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	scratch := testsupport.TempDir(t)

	m := fixedManager(true)
	if _, err := m.Create(envRoot(scratch, repoDir, dataDir, "s1"), CreateArgs{Name: "feat"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := m.List(envRoot(scratch, repoDir, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.CWDWorktree != nil {
		t.Errorf("cwd_worktree should be nil when real cwd is outside the repo, got %q", *res.CWDWorktree)
	}
}

// TestRepoRootInvalidErrorsNamesRepoRoot: a repo_root that isn't a git repo
// fails with a message naming repo_root (not cwd), so the agent can tell which
// path it pointed at was wrong.
func TestRepoRootInvalidErrorsNamesRepoRoot(t *testing.T) {
	notARepo := testsupport.TempDir(t)
	dataDir := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)

	_, err := fixedManager(true).List(envRoot(cwd, notARepo, dataDir, "s1"), ListFilter{})
	if err == nil || !strings.Contains(err.Error(), "repo_root") {
		t.Errorf("expected a repo_root-named not-a-repo error, got %v", err)
	}
}

// --- §3 scan-and-hint: nearbyRepos -----------------------------------------

func TestNearbyReposFindsChild(t *testing.T) {
	scratch := testsupport.TempDir(t)
	initChildRepo(t, scratch, "terva")

	got := nearbyRepos(scratch)
	if len(got) != 1 || got[0] != "./terva" {
		t.Fatalf("nearbyRepos = %v, want [./terva]", got)
	}
}

func TestNearbyReposMultipleSortedByName(t *testing.T) {
	scratch := testsupport.TempDir(t)
	// Create out of order; ReadDir sorts, so output must be deterministic.
	initChildRepo(t, scratch, "repo-b")
	initChildRepo(t, scratch, "repo-a")

	got := nearbyRepos(scratch)
	want := []string{"./repo-a", "./repo-b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("nearbyRepos = %v, want %v", got, want)
	}
}

// A linked worktree's .git is a *file*, not a dir; it must still be found.
func TestNearbyReposFindsLinkedWorktree(t *testing.T) {
	main, _ := newRepo(t)
	scratch := testsupport.TempDir(t)
	linked := filepath.Join(scratch, "linked")
	runT(t, main, "worktree", "add", "-q", linked)

	if fi, err := os.Lstat(filepath.Join(linked, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("expected a .git file in the linked worktree (err=%v)", err)
	}
	got := nearbyRepos(scratch)
	if len(got) != 1 || got[0] != "./linked" {
		t.Fatalf("nearbyRepos = %v, want [./linked]", got)
	}
}

// A stray, empty .git dir that isn't a real checkout must be rejected by the
// confirmation probe, not surfaced as a repo.
func TestNearbyReposIgnoresStrayDotGit(t *testing.T) {
	scratch := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(scratch, "fake", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := nearbyRepos(scratch); len(got) != 0 {
		t.Fatalf("nearbyRepos = %v, want none (stray .git is not a repo)", got)
	}
}

// A bare repo child has no .git entry, so the pre-filter skips it (and a bare
// repo is a poor /cd target anyway).
func TestNearbyReposSkipsBareRepo(t *testing.T) {
	scratch := testsupport.TempDir(t)
	bare := filepath.Join(scratch, "mirror.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(bare, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Skipf("git init --bare unsupported: %v", err)
	}
	if got := nearbyRepos(scratch); len(got) != 0 {
		t.Fatalf("nearbyRepos = %v, want none (bare repo skipped)", got)
	}
}

func TestNearbyReposNoneWhenEmpty(t *testing.T) {
	scratch := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(scratch, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nearbyRepos(scratch); got != nil {
		t.Fatalf("nearbyRepos = %v, want nil", got)
	}
}

// --- §3 scan-and-hint: the composed error through the tools ------------------

// With cwd a non-repo holding a child checkout, every tool's error names the
// child by a relative path and points at both recovery levers (/cd, repo_root).
// Verified for the read-only list and a mutating tool (create), since they all
// funnel through resolveRepo.
func TestNotAGitRepoHintsNearbyChild(t *testing.T) {
	scratch := testsupport.TempDir(t)
	initChildRepo(t, scratch, "terva")
	m := fixedManager(true)

	checks := map[string]error{}
	_, checks["list"] = m.List(env(scratch, testsupport.TempDir(t), "s1"), ListFilter{})
	_, checks["create"] = m.Create(env(scratch, testsupport.TempDir(t), "s1"), CreateArgs{Name: "feat"})

	for tool, err := range checks {
		if err == nil {
			t.Errorf("%s: expected an error from a non-repo cwd", tool)
			continue
		}
		msg := err.Error()
		for _, want := range []string{"not a git repository", "./terva", "/cd ./terva", "repo_root"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s error missing %q: %s", tool, want, msg)
			}
		}
	}
}

// No repo nearby: the message must stay byte-for-byte today's bare error so the
// no-repo-nearby case doesn't regress into noisier output.
func TestNotAGitRepoNoNearbyUnchanged(t *testing.T) {
	scratch := testsupport.TempDir(t) // no children at all
	_, err := fixedManager(true).List(env(scratch, testsupport.TempDir(t), "s1"), ListFilter{})
	if err == nil {
		t.Fatal("expected a not-a-git-repo error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "not a git repository (cwd "+scratch+")") {
		t.Errorf("no-nearby message changed shape: %s", msg)
	}
	if strings.Contains(msg, "found") || strings.Contains(msg, "repo_root") {
		t.Errorf("no-nearby message should carry no hint: %s", msg)
	}
}

// --- GitAvailable: the session-context gate predicate ------------------------

// cwd is itself a repo → available.
func TestGitAvailableCwdIsRepo(t *testing.T) {
	repoDir, _ := newRepo(t)
	if !GitAvailable(repoDir) {
		t.Errorf("GitAvailable(%q) = false, want true (cwd is a repo)", repoDir)
	}
}

// cwd is inside a linked worktree (its .git is a file) → available.
func TestGitAvailableInsideLinkedWorktree(t *testing.T) {
	main, _ := newRepo(t)
	scratch := testsupport.TempDir(t)
	linked := filepath.Join(scratch, "linked")
	runT(t, main, "worktree", "add", "-q", linked)
	if !GitAvailable(linked) {
		t.Error("GitAvailable inside a linked worktree should be true")
	}
}

// cwd is NOT a repo but an immediate child is (repo_root-reachable) → available.
func TestGitAvailableNearbyChildRepo(t *testing.T) {
	scratch := testsupport.TempDir(t)
	initChildRepo(t, scratch, "terva")
	if !GitAvailable(scratch) {
		t.Error("GitAvailable should be true when an immediate child is a repo")
	}
}

// A non-repo dir whose only child is a plain dir → not available.
func TestGitAvailableFalseForPlainDir(t *testing.T) {
	scratch := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(scratch, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if GitAvailable(scratch) {
		t.Error("GitAvailable should be false for a non-repo dir with no repo children")
	}
}

func TestGitAvailableFalseForEmptyString(t *testing.T) {
	if GitAvailable("") {
		t.Error(`GitAvailable("") should be false`)
	}
}
