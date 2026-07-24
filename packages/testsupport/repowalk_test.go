package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// walkNames returns the relative paths of every file a repo-wide scan would
// visit under root, i.e. the ones SkipScanDir did not prune.
func walkNames(t *testing.T, root string) []string {
	t.Helper()
	var seen []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return seen
}

func has(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A scan must see the project's own sources and nothing else. The name list is
// the cheap half; the nested-checkout rule is the half that was missing, and it
// is the one that turned a guard into a source of false failures.
func TestSkipScanDirPrunesEverythingButFirstPartySource(t *testing.T) {
	root := TempDir(t)
	write(t, filepath.Join(root, "main.go"), "package main")
	write(t, filepath.Join(root, "packages", "agent", "cli.go"), "package agent")
	for _, dir := range []string{".git", ".claude", "bin", "dist", "node_modules", "vendor"} {
		write(t, filepath.Join(root, dir, "buried.go"), "package buried")
	}

	seen := walkNames(t, root)
	if !has(seen, "main.go") || !has(seen, "packages/agent/cli.go") {
		t.Errorf("first-party sources were pruned: %v", seen)
	}
	for _, dir := range []string{".git", ".claude", "bin", "dist", "node_modules", "vendor"} {
		if has(seen, dir+"/buried.go") {
			t.Errorf("%s was walked into; a scan has no business there", dir)
		}
	}
}

// The failure this predicate exists for: a git worktree kept inside the
// repository is a checkout of ANOTHER commit, held to another commit's
// conventions. Scanning it reports offenders that are real files in a real
// branch and say nothing about the tree under test.
func TestSkipScanDirPrunesANestedCheckout(t *testing.T) {
	root := TempDir(t)
	write(t, filepath.Join(root, "main.go"), "package main")
	// A worktree's .git is a FILE holding a gitdir pointer, not a directory —
	// which is exactly why "skip directories named .git" does not cover this.
	write(t, filepath.Join(root, "wt", "feature", ".git"), "gitdir: /elsewhere/.git/worktrees/feature\n")
	write(t, filepath.Join(root, "wt", "feature", "old.go"), "package old")
	// A plain nested clone, whose .git IS a directory.
	write(t, filepath.Join(root, "fixtures", "clone", ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(root, "fixtures", "clone", "vendored.go"), "package vendored")

	seen := walkNames(t, root)
	if !has(seen, "main.go") {
		t.Fatalf("the repository's own source was pruned: %v", seen)
	}
	if has(seen, "wt/feature/old.go") {
		t.Error("walked into a nested worktree — its files belong to another commit")
	}
	if has(seen, "fixtures/clone/vendored.go") {
		t.Error("walked into a nested clone")
	}
}

// The root's own .git must not prune the scan it is the subject of — the whole
// walk would return nothing and every guard would pass vacuously, which is the
// one failure mode worse than a false red.
func TestSkipScanDirDoesNotPruneTheRootItself(t *testing.T) {
	root := TempDir(t)
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(root, "main.go"), "package main")

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if SkipScanDir(root, root, fs.FileInfoToDirEntry(info)) {
		t.Fatal("the root is a checkout by definition; skipping it scans nothing at all")
	}
	if seen := walkNames(t, root); !has(seen, "main.go") {
		t.Errorf("root sources pruned: %v", seen)
	}
}

// Called on a file, it is a no-op — callers guard with d.IsDir(), and a stray
// call must not prune by name (a file called "bin" or "dist" is just a file).
func TestSkipScanDirIgnoresFiles(t *testing.T) {
	root := TempDir(t)
	write(t, filepath.Join(root, "dist"), "not a directory")
	info, err := os.Stat(filepath.Join(root, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if SkipScanDir(root, filepath.Join(root, "dist"), fs.FileInfoToDirEntry(info)) {
		t.Error("a FILE named dist is not the build output directory")
	}
}
