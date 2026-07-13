package fswalk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The walk semantics themselves (gitignore, nested gitignore, .git pruning,
// dirs-first ordering, caps) are pinned by the FileSuggester suite in
// packages/agent/modes/widgets, which consumes this package. These tests
// cover the surface only fswalk owns: the Dir parameter the daemon serves
// from client input.

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListFlatSubdirRelPaths(t *testing.T) {
	root := testsupport.TempDir(t)
	writeTree(t, root, map[string]string{
		"src/main.go":    "x",
		"src/util/u.go":  "x",
		"top.txt":        "x",
		"src/.gitignore": "", // present but empty; must not hide anything
	})

	entries, truncated, err := List(root, Options{Dir: "src"})
	if err != nil || truncated {
		t.Fatalf("List(src): err=%v truncated=%v", err, truncated)
	}
	var rels []string
	for _, e := range entries {
		rels = append(rels, filepath.ToSlash(e.Rel))
	}
	got := strings.Join(rels, ",")
	// Entries are relative to the ROOT (chip-insertable), dirs first.
	if !strings.Contains(got, "src/util") || !strings.Contains(got, "src/main.go") {
		t.Fatalf("subdir listing wrong: %v", rels)
	}
	if strings.Contains(got, "top.txt") {
		t.Fatalf("subdir listing leaked the parent's entries: %v", rels)
	}
	if !entries[0].IsDir {
		t.Fatalf("dirs should sort first: %v", rels)
	}
}

// Dir comes from a network client; it must not read outside the root.
func TestListRejectsEscapes(t *testing.T) {
	root := testsupport.TempDir(t)
	writeTree(t, root, map[string]string{"a.txt": "x"})

	for _, dir := range []string{"..", "../sibling", "a/../../b", "/etc"} {
		if _, _, err := List(root, Options{Dir: dir}); err == nil {
			t.Fatalf("List accepted escaping dir %q", dir)
		}
	}
	// A cleanable inside path is fine.
	if _, _, err := List(root, Options{Dir: "a/.."}); err != nil {
		t.Fatalf("List rejected the root-equivalent dir a/..: %v", err)
	}
}

// Recursive mode ignores Dir (the whole-tree walk always starts at the
// root, like the TUI's recursive picker) rather than erroring.
func TestListRecursiveIgnoresDir(t *testing.T) {
	root := testsupport.TempDir(t)
	writeTree(t, root, map[string]string{"deep/nested/f.go": "x", "top.txt": "x"})

	entries, _, err := List(root, Options{Dir: "deep", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, e := range entries {
		seen = append(seen, filepath.ToSlash(e.Rel))
	}
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, "top.txt") || !strings.Contains(joined, "deep/nested/f.go") {
		t.Fatalf("recursive walk should cover the whole tree: %v", seen)
	}
}
