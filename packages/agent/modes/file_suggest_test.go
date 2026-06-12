package modes

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestExpandFileChipsAtPickerShape(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "repo")
	in := "read [file:README.md] and [dir:docs/]"
	want := "read " + filepath.Join(cwd, "README.md") + " and " + filepath.Join(cwd, "docs")
	if got := expandFileChips(in, cwd); got != want {
		t.Fatalf("expandFileChips() = %q, want %q", got, want)
	}
}

func TestExpandFileChipsLeavesEditorPlaceholderShapeAlone(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "repo")
	// tui.Editor.SubmitValue should expand [file:N:name] using its
	// private path map before modes see the text. If such a token leaks
	// through, modes must not guess that "1:foo.txt" is a relative path.
	in := "read [file:1:foo.txt]"
	if got := expandFileChips(in, cwd); got != in {
		t.Fatalf("editor placeholder was changed: %q", got)
	}
}

// TestFileSuggesterPicksUpNewEntries pins the cache-invalidation bug
// fix: creating a subdirectory after the picker has already scanned
// the cwd must surface that subdirectory on the next scan, without
// any explicit invalidation call. The cache is keyed on the dir's
// mtime, which the OS bumps on every entry add/remove/rename.
func TestFileSuggesterPicksUpNewEntries(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newFileSuggester()
	s.SetCWD(tmp)

	first := s.scan()
	if !containsEntry(first, "existing.txt", false) {
		t.Fatalf("first scan missing existing.txt: %#v", first)
	}
	if containsEntry(first, "test", true) {
		t.Fatalf("first scan unexpectedly saw the not-yet-created test/: %#v", first)
	}

	// Sleep one filesystem tick: HFS+/APFS/ext4 all bump directory
	// mtime with at-least 1s resolution depending on mount options.
	// Without this sleep on coarse-resolution filesystems the mtime
	// after Mkdir can equal the mtime captured during the first scan
	// and the cache would (correctly) be retained.
	time.Sleep(1100 * time.Millisecond)
	if err := os.Mkdir(filepath.Join(tmp, "test"), 0o755); err != nil {
		t.Fatal(err)
	}

	second := s.scan()
	if !containsEntry(second, "test", true) {
		t.Fatalf("second scan did not pick up the newly created test/: %#v", second)
	}
	if !containsEntry(second, "existing.txt", false) {
		t.Fatalf("second scan dropped existing.txt: %#v", second)
	}

	// Directories sort before files.
	sorted := make([]string, 0, len(second))
	for _, e := range second {
		sorted = append(sorted, e.name)
	}
	if !sort.IsSorted(byDirsFirst(second)) {
		t.Fatalf("entries are not dirs-first / case-insensitive sorted: %v", sorted)
	}
}

// TestFileSuggesterRecursiveHonorsNestedGitignore reproduces the
// reported bug: a vendored tool directory (.opencode) carries its own
// .gitignore that excludes node_modules, but the repo root .gitignore
// says nothing about it. Before nested .gitignore support the
// recursive walk surfaced thousands of node_modules files and a deeply
// nested real source file (eda/rjg/enk-1150/pipeline.py) could not be
// found. The walk must prune the nested-ignored tree and surface the
// deep file.
func TestFileSuggesterRecursiveHonorsNestedGitignore(t *testing.T) {
	tmp := t.TempDir()
	// Root .gitignore knows nothing about node_modules.
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .opencode/.gitignore ignores its own node_modules.
	opencodeNM := filepath.Join(tmp, ".opencode", "node_modules", "zod", "src")
	if err := os.MkdirAll(opencodeNM, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".opencode", ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opencodeNM, "pipeline.test.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The deeply nested real file the user is hunting for.
	deep := filepath.Join(tmp, "eda", "rjg", "enk-1150")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "pipeline.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newFileSuggester()
	s.SetCWD(tmp)
	s.SetRecursive(true)

	all := s.scan()
	for _, e := range all {
		if strings.Contains(filepath.ToSlash(e.rel), "node_modules") {
			t.Fatalf("recursive scan surfaced nested-gitignored node_modules: %#v", e)
		}
	}
	rel := filepath.Join("eda", "rjg", "enk-1150", "pipeline.py")
	if !containsEntry(all, rel, false) {
		t.Fatalf("recursive scan missing deep pipeline.py: %#v", all)
	}
	// The fuzzy query should now find the real file.
	got := s.matches("@pipeline.py")
	if !containsEntry(got, rel, false) {
		t.Fatalf("@pipeline.py did not match the deep file: %#v", got)
	}
}

func containsEntry(entries []fileEntry, name string, isDir bool) bool {
	for _, e := range entries {
		if e.name == name && e.isDir == isDir {
			return true
		}
	}
	return false
}

type byDirsFirst []fileEntry

func (b byDirsFirst) Len() int      { return len(b) }
func (b byDirsFirst) Swap(i, j int) { b[i], b[j] = b[j], b[i] }
func (b byDirsFirst) Less(i, j int) bool {
	if b[i].isDir != b[j].isDir {
		return b[i].isDir
	}
	return b[i].name < b[j].name
}

// TestFileSuggesterFuzzyMatch verifies the @-query ranks entries with
// a fuzzy subsequence match rather than a plain substring, so a
// non-contiguous pattern like "fsg" still finds "file_suggest.go".
func TestFileSuggesterFuzzyMatch(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"file_suggest.go", "interactive.go", "README.md"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newFileSuggester()
	s.SetCWD(tmp)

	got := s.matches("@fsg")
	if !containsEntry(got, "file_suggest.go", false) {
		t.Fatalf("fuzzy query @fsg did not match file_suggest.go: %#v", got)
	}
	if len(got) == 0 || got[0].name != "file_suggest.go" {
		t.Fatalf("file_suggest.go not ranked first for @fsg: %#v", got)
	}
}

// TestFileSuggesterFlatBrowseIntoDirIgnoresStaleFilter reproduces the
// reported bug: in flat (non-recursive) mode, typing "@eda" to find a
// directory then opening it with Right must show that directory's
// contents, not re-apply the "eda" filter inside it (which matches
// nothing). The interactive layer clears the @-query when descending,
// so here we model that by browsing with Right and then matching an
// empty query against the new level.
func TestFileSuggesterFlatBrowseIntoDirIgnoresStaleFilter(t *testing.T) {
	tmp := t.TempDir()
	// eda/rjg/enk-1150 with a file inside, plus a sibling so the filter
	// is meaningful at the top level.
	if err := os.MkdirAll(filepath.Join(tmp, "eda", "rjg", "enk-1150"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "eda", "rjg", "enk-1150", "pipeline.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := newFileSuggester()
	s.SetCWD(tmp) // flat mode

	// Top level: "@eda" highlights the eda/ directory. Render populates
	// lastMatches, which Right/Left act on (it runs every frame before
	// key handling in the live UI).
	s.lastMatches = s.matches("@eda")
	if !containsEntry(s.lastMatches, "eda", true) {
		t.Fatalf("@eda did not match eda/: %#v", s.lastMatches)
	}
	s.cursor = 0 // eda/ is the (only) match, selected.

	// Open it. After the interactive layer clears the query, the picker
	// is browsing eda/ with an empty filter and must show rjg/.
	if !s.Right() {
		t.Fatal("Right() did not open eda/")
	}
	if got := s.matches("@"); !containsEntry(got, "rjg", true) {
		t.Fatalf("after opening eda/, empty filter did not show rjg/: %#v", got)
	}
	// The stale filter would have shown nothing: confirm that's the
	// behavior the fix avoids.
	if got := s.matches("@eda"); len(got) != 0 {
		t.Fatalf("stale @eda filter inside eda/ unexpectedly matched: %#v", got)
	}
}

// TestFileSuggesterRecursiveMatch verifies recursive mode flattens the
// tree and matches against the cwd-relative path, so a pattern can
// span directory boundaries.
func TestFileSuggesterRecursiveMatch(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "src", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "foo", "bar.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp)
	s.SetRecursive(true)

	rel := filepath.Join("src", "foo", "bar.go")
	got := s.matches("@foobar")
	if !containsEntry(got, rel, false) {
		t.Fatalf("recursive @foobar did not match %s: %#v", rel, got)
	}
}

// TestFileSuggesterRecursiveSkipsHeavyDirs ensures the walk prunes
// directories like .git that would otherwise dominate the budget.
func TestFileSuggesterRecursiveSkipsHeavyDirs(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".git", "objects", "deadbeef"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp)
	s.SetRecursive(true)

	all := s.scan()
	for _, e := range all {
		if e.rel == ".git" || strings.HasPrefix(e.rel, ".git"+string(filepath.Separator)) {
			t.Fatalf("recursive scan descended into .git: %#v", e)
		}
	}
	if !containsEntry(all, "main.go", false) {
		t.Fatalf("recursive scan missing main.go: %#v", all)
	}
}

// TestFileSuggesterRecursiveHonorsGitignore ensures the recursive walk
// prunes anything listed in the project's root .gitignore — build
// outputs, dependency dirs, and IaC tool caches like
// .terraform/.terragrunt-cache — while still surfacing tracked files.
func TestFileSuggesterRecursiveHonorsGitignore(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"),
		[]byte(".terraform/\n.terragrunt-cache/\nnode_modules/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := []string{".terraform", ".terragrunt-cache", "node_modules"}
	for _, dir := range ignored {
		nested := filepath.Join(tmp, dir, "deep")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "junk"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "debug.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.tf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp)
	s.SetRecursive(true)

	all := s.scan()
	for _, e := range all {
		for _, skip := range ignored {
			if e.rel == skip || strings.HasPrefix(e.rel, skip+string(filepath.Separator)) {
				t.Fatalf("recursive scan descended into gitignored %s: %#v", skip, e)
			}
		}
		if e.rel == "debug.log" {
			t.Fatalf("recursive scan surfaced gitignored *.log file: %#v", e)
		}
	}
	if !containsEntry(all, "main.tf", false) {
		t.Fatalf("recursive scan missing tracked main.tf: %#v", all)
	}
}

// TestFileSuggesterFlatModeHonorsGitignore verifies the default
// directory-by-directory browse also hides gitignored entries and
// always hides .git.
func TestFileSuggesterFlatModeHonorsGitignore(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("node_modules/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "debug.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp) // flat mode, respectGitignore on by default

	all := s.scan()
	if containsEntry(all, "node_modules", true) {
		t.Fatal("flat scan surfaced gitignored node_modules/")
	}
	if containsEntry(all, ".git", true) {
		t.Fatal("flat scan surfaced .git/")
	}
	if containsEntry(all, "debug.log", false) {
		t.Fatal("flat scan surfaced gitignored *.log file")
	}
	if !containsEntry(all, "main.go", false) {
		t.Fatalf("flat scan missing tracked main.go: %#v", all)
	}
	// .gitignore itself is not ignored, so it should remain visible.
	if !containsEntry(all, ".gitignore", false) {
		t.Fatalf("flat scan missing .gitignore: %#v", all)
	}
}

// TestFileSuggesterRespectGitignoreToggle verifies disabling the
// setting surfaces gitignored entries in both modes (while .git stays
// hidden in recursive mode to protect the entry budget).
func TestFileSuggesterRespectGitignoreToggle(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp)

	// On by default: dist/ hidden.
	if containsEntry(s.scan(), "dist", true) {
		t.Fatal("dist/ should be hidden while respectGitignore is on")
	}
	// Toggle off: dist/ visible (flat mode).
	s.SetRespectGitignore(false)
	if !containsEntry(s.scan(), "dist", true) {
		t.Fatal("dist/ should be visible after disabling respectGitignore (flat)")
	}
	// And in recursive mode.
	s.SetRecursive(true)
	if !containsEntry(s.scan(), "dist", true) {
		t.Fatal("dist/ should be visible after disabling respectGitignore (recursive)")
	}
}

// TestFileSuggesterToggleResetsCache verifies SetRecursive drops the
// cached scan so the next matches() reflects the new mode.
func TestFileSuggesterToggleResetsCache(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "nested.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFileSuggester()
	s.SetCWD(tmp)

	rel := filepath.Join("pkg", "nested.go")
	if containsEntry(s.matches("@nested"), rel, false) {
		t.Fatal("flat mode unexpectedly saw nested.go")
	}
	s.SetRecursive(true)
	if !containsEntry(s.matches("@nested"), rel, false) {
		t.Fatal("recursive mode did not surface nested.go after toggle")
	}
}
