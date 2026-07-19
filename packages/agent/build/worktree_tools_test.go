package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// gitRepoDir makes a minimal real repo — the registration gate probes git for
// real, so the test does too.
func gitRepoDir(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

var worktreeToolNames = []string{"worktree_list", "worktree_create", "worktree_claim", "worktree_release", "worktree_remove"}

// The worktree tools register exactly where they can work: present in a git
// repo cwd, absent in a plain directory (a session outside any repo pays no
// tokens for them — the native twin of the extension's tool withdrawal).
func TestWorktreeToolsRegisterOnlyInGitRepos(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := gitRepoDir(t)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, repo, nil, "", "", false, nil)
	for _, name := range worktreeToolNames {
		if _, ok := reg[name]; !ok {
			t.Errorf("%s missing from a git-repo session", name)
		}
	}

	plain := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
	for _, name := range worktreeToolNames {
		if _, ok := plain[name]; ok {
			t.Errorf("%s registered outside any git repo", name)
		}
	}
}

// Plan mode promises read-only: worktree_list alone survives the prune; the
// four mutating siblings are not even visible.
func TestWorktreeToolsPlanModeKeepsOnlyList(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := gitRepoDir(t)

	plan := BuildToolRegistry(Args{}, core.ApprovalPlan, repo, nil, "", "", false, nil)
	if _, ok := plan["worktree_list"]; !ok {
		t.Error("worktree_list should survive plan mode (read-only)")
	}
	for _, name := range []string{"worktree_create", "worktree_claim", "worktree_release", "worktree_remove"} {
		if _, ok := plan[name]; ok {
			t.Errorf("%s must be pruned in plan mode", name)
		}
	}
}
