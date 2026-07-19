package worktree

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// runGit runs `git -C <dir> <args...>` and returns trimmed stdout. Stderr is
// folded into the error so callers (and the model) see why git refused. We
// always pass an explicit -C and never rely on the process cwd (it's the
// extension's install dir).
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitWorktree is one entry from `git worktree list --porcelain`.
type gitWorktree struct {
	Path     string
	Head     string
	Branch   string // short name (no refs/heads/), "" when detached/bare
	Detached bool
	Bare     bool
	Prunable bool
}

// listWorktrees returns git's view of every worktree, keyed by canonical path.
// This is the source of truth for *existence*; registry.json augments it.
func listWorktrees(cwd string) (map[string]gitWorktree, error) {
	out, err := runGit(cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parsePorcelain(out), nil
}

// parsePorcelain parses `git worktree list --porcelain` output into a map keyed
// by canonical worktree path. Split out from listWorktrees so it can be tested
// against fixtures (detached / bare / prunable) without a live repo.
func parsePorcelain(out string) map[string]gitWorktree {
	res := map[string]gitWorktree{}
	var cur *gitWorktree
	flush := func() {
		if cur != nil && cur.Path != "" {
			res[canonPath(cur.Path)] = *cur
		}
		cur = nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &gitWorktree{Path: val}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	return res
}

// headCommit returns the worktree's current HEAD sha (best-effort).
func headCommit(dir string) string {
	h, _ := runGit(dir, "rev-parse", "HEAD")
	return h
}

// currentRef returns the abbreviated current branch name, or "HEAD" when
// detached or unknown — used as the friendly base_ref when no base is given.
func currentRef(dir string) string {
	r, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || r == "" {
		return "HEAD"
	}
	return r
}

// resolveCommit resolves a ref/sha to a commit sha, failing on a bad ref.
func resolveCommit(dir, ref string) (string, error) {
	return runGit(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// branchExists reports whether a local branch of that name is present. Used to
// resume a branch left behind by a prior `worktree_remove` (which keeps the
// branch by default) instead of failing on `git worktree add -b`.
func branchExists(dir, branch string) bool {
	_, err := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// worktreeDirty reports whether the worktree has uncommitted changes.
func worktreeDirty(dir string) (bool, error) {
	out, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// hasUnmergedWork reports whether a worktree carries commits that would be lost
// on removal. Best-effort: if the branch tracks an upstream, "unpushed" is any
// commit in @{upstream}..HEAD; otherwise any commit beyond base_commit counts as
// unmerged local work. Returns a short human reason on true.
func hasUnmergedWork(dir, baseCommit string) (bool, string) {
	if up, err := runGit(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && up != "" {
		if n, err := runGit(dir, "rev-list", "--count", "@{upstream}..HEAD"); err == nil && n != "" && n != "0" {
			return true, fmt.Sprintf("%s commit(s) not pushed to %s", n, up)
		}
		return false, ""
	}
	if baseCommit != "" {
		if n, err := runGit(dir, "rev-list", "--count", baseCommit+"..HEAD"); err == nil && n != "" && n != "0" {
			return true, fmt.Sprintf("%s commit(s) beyond base not pushed or merged", n)
		}
	}
	return false, ""
}

// aheadCommits returns how many commits HEAD is ahead of base and the oneline
// subjects of up to max of them (newest first). Best-effort: returns (0, nil) on
// any git error or when base is empty.
func aheadCommits(dir, base string, max int) (int, []string) {
	if base == "" {
		return 0, nil
	}
	out, err := runGit(dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n == 0 {
		return n, nil
	}
	args := []string{"log", "--oneline", "--no-decorate"}
	if max > 0 {
		args = append(args, fmt.Sprintf("-%d", max))
	}
	args = append(args, base+"..HEAD")
	logOut, err := runGit(dir, args...)
	if err != nil || logOut == "" {
		return n, nil
	}
	return n, strings.Split(logOut, "\n")
}

// topLevel returns the working-tree root of dir (the worktree cwd is in), used
// to report which listed worktree the agent is currently inside.
func topLevel(dir string) string {
	t, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return canonPath(t)
}
