package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Reclaim is the unattended counterpart to Remove: it runs when a swarm
// sub-agent exits, with nobody watching. That changes what correctness means.
// Remove may refuse loudly; Reclaim must decide, and every wrong decision is
// either litter (kept what it should have dropped) or lost work (dropped what
// it should have kept). These tests pin the second kind hardest.

// TestReclaimRemovesACleanWorktreeAndItsEmptyBranch is the case that motivates
// the whole feature: a sub-agent that read files and wrote nothing. There is no
// content anywhere in it, so both the checkout and its never-committed branch go.
func TestReclaimRemovesACleanWorktreeAndItsEmptyBranch(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	e := env(repoDir, dataDir, "s1")

	res, err := m.Create(e, CreateArgs{Name: "clean", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	branch := res.Branch

	got, err := m.Reclaim(e, ReclaimArgs{Name: "clean"})
	if err != nil {
		t.Fatalf("reclaiming a clean worktree must not error: %v", err)
	}
	if !got.Removed {
		t.Errorf("a clean worktree was kept (reason %q); it holds nothing", got.Reason)
	}
	if !got.BranchDeleted {
		t.Error("the branch never carried a commit and should have gone with the checkout")
	}
	if dirExists(res.Path) {
		t.Errorf("checkout still on disk at %s", res.Path)
	}
	if branchExists(repoDir, branch) {
		t.Errorf("branch %s survived despite having no commits", branch)
	}
	// The registry entry must go too, or the worktree is "removed" but still
	// listed — the exact half-state that leaves 44 ghosts behind.
	lst, err := m.List(e, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range lst.Worktrees {
		if it != nil && it.Name == "clean" && !it.Unmanaged {
			t.Error("registry still lists the reclaimed worktree")
		}
	}
}

// TestReclaimKeepsUncommittedChanges: edits that were never committed exist
// nowhere else in the world. Reclaim must keep them and must not report the
// refusal as an error, because the caller fires this blindly on every exit.
func TestReclaimKeepsUncommittedChanges(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	e := env(repoDir, dataDir, "s1")

	res, err := m.Create(e, CreateArgs{Name: "dirty", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "wip.txt"), []byte("half a thought\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := m.Reclaim(e, ReclaimArgs{Name: "dirty"})
	if err != nil {
		t.Fatalf("a kept worktree is a normal outcome, not an error: %v", err)
	}
	if got.Removed {
		t.Fatal("uncommitted work was deleted")
	}
	if !strings.Contains(got.Reason, "uncommitted") {
		t.Errorf("reason %q does not say why it was kept", got.Reason)
	}
	if !dirExists(res.Path) {
		t.Error("checkout was removed despite being kept")
	}
	if !branchExists(repoDir, res.Branch) {
		t.Error("branch was deleted despite the worktree being kept")
	}
}

// TestReclaimKeepsCommitsThatExistOnlyHere: committed but with no remote to
// hold them. The commits are reachable only from this branch, so dropping the
// checkout would strand them behind a branch name nobody is looking at.
func TestReclaimKeepsCommitsThatExistOnlyHere(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	e := env(repoDir, dataDir, "s1")

	res, err := m.Create(e, CreateArgs{Name: "local", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "done.txt"), []byte("real work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, res.Path, "add", "-A")
	runT(t, res.Path, "commit", "-qm", "real work")

	got, err := m.Reclaim(e, ReclaimArgs{Name: "local"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Removed {
		t.Fatal("a commit that exists nowhere else was deleted")
	}
	if !strings.Contains(got.Reason, "beyond base") {
		t.Errorf("reason %q does not name the unmerged commit", got.Reason)
	}
	if !dirExists(res.Path) {
		t.Error("checkout was removed despite carrying an unpushed commit")
	}
}

// TestReclaimRemovesPushedWorkButKeepsTheBranch is the interesting half of the
// policy. Once the commits are on a remote the CHECKOUT is expendable — that is
// the point of asking @{upstream}..HEAD rather than counting commits. But the
// branch still names real history, so it stays as the handle to find it again.
func TestReclaimRemovesPushedWorkButKeepsTheBranch(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	e := env(repoDir, dataDir, "s1")

	res, err := m.Create(e, CreateArgs{Name: "pushed", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "shipped.txt"), []byte("shipped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, res.Path, "add", "-A")
	runT(t, res.Path, "commit", "-qm", "shipped")

	remote := testsupport.TempDir(t)
	runT(t, remote, "init", "-q", "--bare")
	runT(t, res.Path, "remote", "add", "origin", remote)
	runT(t, res.Path, "push", "-q", "-u", "origin", res.Branch)

	got, err := m.Reclaim(e, ReclaimArgs{Name: "pushed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Removed {
		t.Fatalf("pushed work does not need its checkout kept (reason %q)", got.Reason)
	}
	if got.BranchDeleted {
		t.Error("the branch carried commits and must survive the checkout")
	}
	if !branchExists(repoDir, res.Branch) {
		t.Errorf("branch %s was deleted despite carrying commits", res.Branch)
	}
}

// TestReclaimKeepsTheBranchWhenBaseIsUnknown pins the rule that an
// unmeasurable worktree is never destroyed further than necessary. With no
// base_commit — a legacy or hand-edited registry — nothing can say whether the
// branch ever committed, so it is left alone rather than guessed at.
func TestReclaimKeepsTheBranchWhenBaseIsUnknown(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	e := env(repoDir, dataDir, "s1")

	res, err := m.Create(e, CreateArgs{Name: "nobase", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	// Blank the recorded base, as an older registry would have it.
	r, err := resolveRepo(e)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := loadRegistry(r)
	if err != nil {
		t.Fatal(err)
	}
	reg.Worktrees["nobase"].BaseCommit = ""
	if err := saveRegistry(r.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	got, err := m.Reclaim(e, ReclaimArgs{Name: "nobase"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Removed {
		t.Fatalf("a clean checkout should still be reclaimed (reason %q)", got.Reason)
	}
	if got.BranchDeleted {
		t.Error("with no base to measure against, the branch must be left alone")
	}
	if !branchExists(repoDir, res.Branch) {
		t.Errorf("branch %s was deleted on an unmeasurable worktree", res.Branch)
	}
}

// TestReclaimUnknownWorktreeErrors: a name the registry never had is a caller
// bug, not a tidy-up outcome, so it errors rather than reporting Removed=false.
func TestReclaimUnknownWorktreeErrors(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Reclaim(env(repoDir, dataDir, "s1"), ReclaimArgs{Name: "never-existed"}); err == nil {
		t.Error("reclaiming an unknown worktree should error")
	}
}
