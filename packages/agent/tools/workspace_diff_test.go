package tools

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// changeIndex turns a []FileChange into path -> kind for easy assertions.
func changeIndex(changes []FileChange) map[string]string {
	m := make(map[string]string, len(changes))
	for _, c := range changes {
		m[c.Path] = c.Kind
	}
	return m
}

// A full add/modify/delete cycle is reported with the right kinds, and an
// untouched file is omitted.
func TestWorkspaceDifferAddModifyDelete(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeFile(t, filepath.Join(dir, "keep.txt"), "stable")
	writeFile(t, filepath.Join(dir, "edit.txt"), "before")
	writeFile(t, filepath.Join(dir, "gone.txt"), "doomed")

	d := NewWorkspaceDiffer(func() string { return dir })
	d.Rebase()

	// Mutate the tree: add, modify (size grows so the stamp differs
	// regardless of mtime granularity), delete; leave keep.txt alone.
	writeFile(t, filepath.Join(dir, "sub", "new.txt"), "fresh")
	writeFile(t, filepath.Join(dir, "edit.txt"), "before-and-after")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	got := changeIndex(d.Diff())
	want := map[string]string{
		"sub/new.txt": "added",
		"edit.txt":    "modified",
		"gone.txt":    "deleted",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Errorf("%s: got %q, want %q", path, got[path], kind)
		}
	}
	if _, ok := got["keep.txt"]; ok {
		t.Error("untouched keep.txt must not appear in the diff")
	}
}

// No mutation between Rebase and Diff yields no changes.
func TestWorkspaceDifferNoChanges(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "a")

	d := NewWorkspaceDiffer(func() string { return dir })
	d.Rebase()
	if changes := d.Diff(); len(changes) != 0 {
		t.Errorf("expected no changes, got %v", changes)
	}
}

// Diff before any Rebase reports nothing (no baseline to measure against).
func TestWorkspaceDifferNoBaseline(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "a")
	d := NewWorkspaceDiffer(func() string { return dir })
	if changes := d.Diff(); changes != nil {
		t.Errorf("Diff without Rebase should report nothing, got %v", changes)
	}
}

// .gitignore'd paths and .git/ are excluded from the diff, so build output
// and repo metadata never show up as changes.
func TestWorkspaceDifferHonorsGitignoreAndGit(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.log\nbuild/\n")
	writeFile(t, filepath.Join(dir, "src.go"), "package x")

	d := NewWorkspaceDiffer(func() string { return dir })
	d.Rebase()

	// Touch a tracked file (should report) and several ignored locations
	// (should not): a gitignored file, a gitignored dir, and .git/.
	writeFile(t, filepath.Join(dir, "src.go"), "package x // edited")
	writeFile(t, filepath.Join(dir, "ignored.log"), "noise")
	writeFile(t, filepath.Join(dir, "build", "out.bin"), "artifact")
	writeFile(t, filepath.Join(dir, ".git", "COMMIT_EDITMSG"), "wip")

	got := changeIndex(d.Diff())
	if got["src.go"] != "modified" {
		t.Errorf("src.go should be modified, got %q", got["src.go"])
	}
	for _, p := range []string{"ignored.log", "build/out.bin", ".git/COMMIT_EDITMSG"} {
		if kind, ok := got[p]; ok {
			t.Errorf("%s must be excluded, got %q", p, kind)
		}
	}
}

// A re-Rebase resets the baseline: changes already reported don't resurface.
func TestWorkspaceDifferRebaseResets(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "a")

	d := NewWorkspaceDiffer(func() string { return dir })
	d.Rebase()
	writeFile(t, filepath.Join(dir, "b.txt"), "b")
	if got := changeIndex(d.Diff()); got["b.txt"] != "added" {
		t.Fatalf("first diff should see b.txt added, got %v", got)
	}
	// Re-baseline now includes b.txt; a follow-up diff with no new change
	// is empty (b.txt isn't reported twice).
	d.Rebase()
	if changes := d.Diff(); len(changes) != 0 {
		t.Errorf("after rebase, unchanged tree should diff empty, got %v", changes)
	}
}
