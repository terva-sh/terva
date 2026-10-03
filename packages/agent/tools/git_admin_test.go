package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 write and edit refuse git's own files, jailed or not, because a hook, a
// filter, an fsmonitor command, or a repointed .git file makes the next git
// run a command, and write needs no approval in auto-edit. Each case is one
// trap from the probes behind #1572. A control write beside them still works,
// so the refusal is not a blanket failure.
func TestWriteAndEditRefuseGitAdminFiles(t *testing.T) {
	repo := testsupport.TempDir(t)
	for _, d := range []string{".git/hooks", ".git/info", "wt"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		".git/config": "[core]\n",
		"wt/.git":     "gitdir: /elsewhere\n",
		"a.txt":       "a\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	traps := []string{
		".git/hooks/post-checkout",
		".git/info/attributes",
		".git/config",
		".GIT/config",
		"wt/.git",
	}
	// A symlinked directory into .git, and a dangling symlink to a hook that
	// does not exist yet. Writing through the dangling one creates the hook.
	if err := os.Symlink(filepath.Join(repo, ".git", "hooks"), filepath.Join(repo, "hooks-link")); err == nil {
		if err := os.Symlink(filepath.Join(repo, ".git", "hooks", "pre-push"), filepath.Join(repo, "dangle")); err != nil {
			t.Fatal(err)
		}
		// A link whose target climbs out of a symlinked directory with "..":
		// sub is .git/hooks/d, so sub/../pre-merge is .git/hooks/pre-merge,
		// though cleaning the target first would read it as repo/pre-merge.
		if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks", "d"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(repo, ".git", "hooks", "d"), filepath.Join(repo, "sub")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("sub/../pre-merge", filepath.Join(repo, "dotdot")); err != nil {
			t.Fatal(err)
		}
		// The same climb in the argument itself. The tool passes an absolute
		// path to the kernel unchanged, so sub/.. is walked after sub.
		// filepath.Join would clean it away, so the string is built by hand.
		absDotdot := repo + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + "pre-rebase"
		// A link chain through a link named .git whose gitdir has another
		// name: lnk -> a/.git -> gd. The resolved path holds no .git.
		for _, d := range []string{"gd/hooks", "a"} {
			if err := os.MkdirAll(filepath.Join(repo, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join(repo, "gd"), filepath.Join(repo, "a", ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a/.git", filepath.Join(repo, "lnk")); err != nil {
			t.Fatal(err)
		}
		traps = append(traps, "hooks-link/pre-commit", "dangle", "dotdot", absDotdot, "lnk/hooks/post-merge")
	} else {
		t.Logf("symlinks unavailable, so the symlink cases are skipped: %v", err)
	}
	// gd is the gitdir the lnk trap would write into, so it is checked too.
	snap := func() string {
		return gitDirContents(t, filepath.Join(repo, ".git")) + "|" + gitDirContents(t, filepath.Join(repo, "gd"))
	}
	before := snap()
	for _, sandbox := range []*Sandbox{nil, lockedSandbox(repo)} {
		w := &WriteTool{CWD: repo, Sandbox: sandbox}
		e := &EditTool{CWD: repo, Sandbox: sandbox}
		for _, p := range traps {
			_, err := w.Execute(ctx, mustJSON(t, map[string]any{"path": p, "content": "#!/bin/sh\ntouch /tmp/x\n"}), nil)
			if err == nil || !strings.Contains(err.Error(), "git's own files") {
				t.Errorf("write %s (locked=%v): %v", p, sandbox != nil, err)
			}
		}
		for _, p := range []string{".git/config", "wt/.git"} {
			_, err := e.Execute(ctx, mustJSON(t, map[string]any{"path": p,
				"edits": []map[string]string{{"oldText": "\n", "newText": "\n\tfsmonitor = touch /tmp/x\n"}}}), nil)
			if err == nil || !strings.Contains(err.Error(), "git's own files") {
				t.Errorf("edit %s (locked=%v): %v", p, sandbox != nil, err)
			}
		}
		if _, err := w.Execute(ctx, mustJSON(t, map[string]any{"path": "a.txt", "content": "b\n"}), nil); err != nil {
			t.Errorf("a plain write was refused (locked=%v): %v", sandbox != nil, err)
		}
	}
	if after := snap(); after != before {
		t.Fatalf("a refused write changed .git:\nbefore %s\nafter  %s", before, after)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "wt", ".git")); string(got) != "gitdir: /elsewhere\n" {
		t.Fatalf("the worktree .git file changed: %q", got)
	}
}

// gitDirContents lists every file in the fixture's three .git directories
// with its content, so a test can check that nothing was created or changed.
// The fixture has no deeper directories.
func gitDirContents(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, sub := range []string{"", "hooks", "info"} {
		d := filepath.Join(dir, sub)
		ents, err := os.ReadDir(d)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(d, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s/%s=%q;", sub, e.Name(), data)
		}
	}
	return b.String()
}

// Names that only contain ".git" are ordinary files.
func TestGitAdminMatchesWholeComponentsOnly(t *testing.T) {
	for p, want := range map[string]bool{
		"/r/.git/config":    true,
		"/r/wt/.git":        true,
		"/r/.GIT/hooks/x":   true,
		"/r/.gitignore":     false,
		"/r/.github/ci.yml": false,
		"/r/docs/x.git.md":  false,
		"/r/.gitattributes": false,
	} {
		if got := hasGitComponent(p); got != want {
			t.Errorf("hasGitComponent(%q) = %v, want %v", p, got, want)
		}
	}
}

func lockedSandbox(root string) *Sandbox {
	s := NewSandbox(root)
	s.Lock()
	return s
}
