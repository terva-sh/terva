package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// installed runs EnsureInstalled into a scratch dir and returns the set of
// filenames it wrote.
func installed(t *testing.T) (dir string, names map[string]bool) {
	t.Helper()
	dir, err := EnsureInstalled(testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read installed dir: %v", err)
	}
	names = make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	return dir, names
}

// Every page in docs/ must reach disk on an install.
//
// This reads the real docs/ directory rather than the embed: asserting the
// installed set against embeddedDocs would only restate the glob back to itself,
// and would have passed just as happily against the hand-maintained map this
// replaced — the map that quietly dropped fifteen pages, web.md included.
// The source tree is the thing the answer has to agree with.
func TestEveryDocOnDiskIsInstalled(t *testing.T) {
	entries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	_, got := installed(t)

	var missing []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue // subdirectories are the internal tier; they do not ship
		}
		if !got[e.Name()] {
			missing = append(missing, e.Name())
		}
	}
	if len(missing) > 0 {
		t.Errorf("docs present in docs/ but never installed: %v\n"+
			"they ship inside the binary and never reach $TERVA_HOME/docs, so anything "+
			"pointing a reader at them — help text, the system prompt — dangles", missing)
	}
}

// A doc named in the source is a promise to the reader. `terva web --help` says
// "See docs/web.md for deployment"; the system prompt tells the model its docs
// are installed under $TERVA_HOME/docs. Both are lies if the page never shipped,
// and that is exactly how an operator ends up filing a bug against a defence
// that was working as designed.
//
// Only top-level docs/*.md count. A reference carrying a further slash
// (docs/plans/..., docs/proposals/...) points into the internal tier, which
// deliberately stays out of the installed set. Tests are skipped too: their
// docs/ paths are invented fixtures inside a temp dir, not pointers at terva's
// own documentation.
func TestDocsReferencedInSourceAreInstalled(t *testing.T) {
	ref := regexp.MustCompile(`docs/([A-Za-z0-9_-]+\.md)`)
	_, got := installed(t)

	type site struct{ file, doc string }
	var dangling []site
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored/generated trees are not ours to police, and .claude
			// holds scratch worktrees of this very repo.
			switch d.Name() {
			case "node_modules", ".git", ".claude", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range ref.FindAllSubmatch(src, -1) {
			doc := string(m[1])
			if !got[doc] {
				dangling = append(dangling, site{path, doc})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, s := range dangling {
		t.Errorf("%s points at docs/%s, which no install ever writes", s.file, s.doc)
	}
}

// docs/README.md is the index, and it links to its siblings by bare filename —
// so the flat install directory is what makes those links resolve. Losing it
// would leave the installed docs with no way in.
func TestIndexIsInstalled(t *testing.T) {
	dir, got := installed(t)
	if !got["README.md"] {
		t.Fatal("no README.md installed: the docs index is the entry point")
	}
	body, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "terva documentation") {
		t.Error("installed README.md is not the docs index — a flat mirror of docs/ " +
			"should put docs/README.md, not the repo's landing page, at the entry point")
	}
}

// Re-installing must not rewrite matching files: EnsureInstalled runs on every
// single startup.
func TestInstallIsIdempotent(t *testing.T) {
	home := testsupport.TempDir(t)
	dir, err := EnsureInstalled(home)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "cli.md")
	before, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureInstalled(home); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a second install rewrote an already-current doc")
	}
}

// A stale doc is a wrong doc: an upgrade whose content changed must land.
func TestOutdatedDocIsRefreshed(t *testing.T) {
	home := testsupport.TempDir(t)
	dir, err := EnsureInstalled(home)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "cli.md")
	if err := os.WriteFile(stale, []byte("# from an older terva\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureInstalled(home); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "from an older terva") {
		t.Error("EnsureInstalled left a stale doc in place")
	}
}
