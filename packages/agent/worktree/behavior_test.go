package worktree

import (
	"terva.sh/terva/packages/testsupport"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewManagerEndToEnd(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := NewManager()
	res, err := m.Create(env(repoDir, dataDir, "real-sess"), CreateArgs{Name: "live", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "claimed" || res.ClaimedBy != "self" {
		t.Errorf("create: %+v", res)
	}
	lst, err := m.List(env(repoDir, dataDir, "real-sess"), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// With the real pidAlive and the current session, our own claim is self.
	if len(lst.Worktrees) != 1 || *lst.Worktrees[0].ClaimedBy != "self" {
		t.Errorf("list: %+v", lst.Worktrees)
	}
	if _, err := m.Remove(env(repoDir, dataDir, "real-sess"), RemoveArgs{Name: "live"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestResolveRepoGuards(t *testing.T) {
	if _, err := resolveRepo(Env{Root: testsupport.TempDir(t), CWD: ""}); err == nil {
		t.Error("empty CWD should error")
	}
	if _, err := resolveRepo(Env{Root: "", CWD: testsupport.TempDir(t)}); err == nil {
		t.Error("nil FS should error")
	}
}

func TestCreateIdempotentSameSession(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	first, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "dup", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "dup", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Path != first.Path || second.ClaimedBy != "self" {
		t.Errorf("same-session re-create should return the same worktree reused: %+v", second)
	}
}

func TestCreateResumesLeftoverBranch(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	first, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "resume", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	// Remove keeps the branch by default.
	if _, err := m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "resume", Force: true}); err != nil {
		t.Fatal(err)
	}
	if b := runT(t, repoDir, "branch", "--list", "wt/resume"); !strings.Contains(b, "wt/resume") {
		t.Fatalf("precondition: branch should remain after remove, got %q", b)
	}
	// Re-creating the same name must attach the leftover branch, not fail on -b.
	again, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "resume", ReuseIfAvailable: true})
	if err != nil {
		t.Fatalf("re-create should resume the leftover branch, got: %v", err)
	}
	if again.Branch != "wt/resume" || again.BaseRef != "wt/resume" {
		t.Errorf("resumed worktree should report base_ref=branch: %+v", again)
	}
	if again.Path != first.Path {
		t.Errorf("resumed path mismatch: %q vs %q", again.Path, first.Path)
	}
	if cur := runT(t, again.Path, "rev-parse", "--abbrev-ref", "HEAD"); cur != "wt/resume" {
		t.Errorf("resumed worktree HEAD should be on wt/resume, got %q", cur)
	}
}

func TestCreateLeftoverBranchBaseConflict(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "c", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "c", Force: true}); err != nil {
		t.Fatal(err)
	}
	// Branch lingers; an explicit base can't be applied → clear conflict error.
	_, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "c", Base: "main"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("explicit base over a leftover branch should conflict, got %v", err)
	}
}

func TestCreateOntoUnmanagedPathErrors(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	r, err := resolveRepo(env(repoDir, dataDir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	// Plant a worktree out-of-band at the exact path Create would use for "rogue".
	target := r.worktreePath("rogue")
	runT(t, repoDir, "worktree", "add", "--quiet", target, "-b", "some-branch")
	// Create must refuse: the path is occupied by a worktree we don't manage.
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "rogue", ReuseIfAvailable: true}); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Errorf("create onto an unmanaged worktree path should error, got %v", err)
	}
}

func TestRemoveCleanFreshSucceeds(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "fresh", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// No commits beyond base, clean tree → removes without force.
	rm, err := m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "fresh"})
	if err != nil || !rm.Removed {
		t.Fatalf("clean fresh worktree should remove without force: rm=%+v err=%v", rm, err)
	}
}

func TestRemoveNonexistent(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	_, err := fixedManager(true).Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "no managed worktree") {
		t.Errorf("removing an unknown worktree should error, got %v", err)
	}
}

func TestRemoveRecoversWhenDirDeletedOutOfBand(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "vanish", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	// Nuke the checkout directly; git still has bookkeeping for it.
	if err := os.RemoveAll(res.Path); err != nil {
		t.Fatal(err)
	}
	// Even without force, a vanished worktree should be cleaned up gracefully.
	rm, err := m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "vanish"})
	if err != nil || !rm.Removed {
		t.Fatalf("vanished worktree should clean up: rm=%+v err=%v", rm, err)
	}
	lst, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{})
	if len(lst.Worktrees) != 0 {
		t.Errorf("registry entry should be dropped after recovery: %+v", lst.Worktrees)
	}
}

func TestListMultipleWorktreesSorted(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	for _, n := range []string{"charlie", "alpha", "bravo"} {
		if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: n, ReuseIfAvailable: true}); err != nil {
			t.Fatal(err)
		}
	}
	lst, err := m.List(env(repoDir, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, it := range lst.Worktrees {
		if it.Status != "claimed" || it.ClaimedBy == nil || *it.ClaimedBy != "self" {
			t.Errorf("each managed worktree should be claimed by self: %+v", it)
		}
		got = append(got, it.Name)
	}
	want := []string{"alpha", "bravo", "charlie"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("worktrees should be sorted by name: got %v want %v", got, want)
	}
}

func TestListEmpty(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	lst, err := fixedManager(true).List(env(repoDir, dataDir, "s1"), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lst.Worktrees) != 0 {
		t.Errorf("fresh repo should have no managed worktrees: %+v", lst.Worktrees)
	}
	if lst.RepoKey == "" {
		t.Error("repo_key should be set even when empty")
	}
	if lst.CWDWorktree != nil {
		t.Errorf("cwd_worktree should be null at repo root, got %v", *lst.CWDWorktree)
	}
}

func TestCorruptRegistrySurfacesError(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "x", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	r, err := resolveRepo(env(repoDir, dataDir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.registryPath(), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(env(repoDir, dataDir, "s1"), ListFilter{}); err == nil || !strings.Contains(err.Error(), "parse registry") {
		t.Errorf("corrupt registry should surface a parse error, got %v", err)
	}
}

// TestRemoveUnmergedWithUpstream exercises the upstream branch of
// hasUnmergedWork: a fully-pushed branch removes cleanly; an unpushed commit on
// a tracked branch is refused without force.
func TestRemoveUnmergedWithUpstream(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "u", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	remote := testsupport.TempDir(t)
	runT(t, remote, "init", "-q", "--bare")
	runT(t, res.Path, "remote", "add", "origin", remote)
	runT(t, res.Path, "push", "-q", "-u", "origin", "wt/u")

	// Fully pushed → @{upstream}..HEAD is empty → removes without force.
	if _, err := m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "u"}); err != nil {
		t.Fatalf("fully-pushed worktree should remove without force: %v", err)
	}

	// Re-create (resumes wt/u, which still tracks origin/wt/u) and commit
	// without pushing → refused without force.
	res2, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "u", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res2.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, res2.Path, "add", "-A")
	runT(t, res2.Path, "commit", "-qm", "unpushed")
	_, err = m.Remove(env(repoDir, dataDir, "s1"), RemoveArgs{Name: "u"})
	if err == nil || !strings.Contains(err.Error(), "not pushed") {
		t.Errorf("unpushed commit should refuse remove, got %v", err)
	}
}
