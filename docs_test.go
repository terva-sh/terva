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

// internalSubdirs are the docs/ subdirectories that deliberately do NOT ship,
// each with the reason. Every one of these is also stripped from the public
// release tree by scripts/release.sh, which is the same call made twice: they
// name internal infrastructure, so neither the binary nor the public repo
// carries them.
//
// This list is an allow-list, not an inventory: TestEveryDocsSubdirIsClassified
// fails on a subdirectory that appears in neither this map nor shippedSubdirs,
// and on a stale entry naming a directory that no longer exists. Adding a docs
// subdirectory therefore forces the ship/don't-ship decision rather than
// defaulting to one.
var internalSubdirs = map[string]string{
	"architecture": "the Go-level implementation tier; cites symbols and internal review findings",
	"decisions":    "decision records; cite internal plans, reviews and infrastructure",
	"ideas":        "the idea ledger: unbuilt ideas nobody has committed to, and the reasoning behind recorded declines",
	"plans":        "active work plans and the roadmap",
	"proposals":    "design proposals, many unimplemented",
	"reviews":      "point-in-time whole-project review findings",
	"vanity":       "terva.sh site sources, not documentation",
}

// installed runs EnsureInstalled into a scratch dir and returns the set of
// pages it wrote, keyed by slash-separated path relative to the docs root
// ("cli.md", "design/README.md").
func installed(t *testing.T) (dir string, names map[string]bool) {
	t.Helper()
	dir, err := EnsureInstalled(testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	names = map[string]bool{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		names[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk installed dir: %v", err)
	}
	return dir, names
}

// Every subdirectory of docs/ must be classified as shipped or internal.
//
// "A subdirectory is internal" used to be the rule, and it was load-bearing:
// the embed glob relied on it. design/ and practices/ broke it — they are
// shipped documentation that outgrew one file — so the rule became a decision,
// and a decision nobody is forced to make is one that gets made by accident.
// The failure this forecloses is silent in the worst direction: a new public
// tier that compiles, passes every other test, and simply never reaches a
// reader's $TERVA_HOME/docs.
func TestEveryDocsSubdirIsClassified(t *testing.T) {
	entries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	shipped := map[string]bool{}
	for _, s := range shippedSubdirs {
		shipped[s] = true
	}

	onDisk := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		onDisk[e.Name()] = true
		if shipped[e.Name()] {
			continue
		}
		if _, ok := internalSubdirs[e.Name()]; ok {
			continue
		}
		t.Errorf("docs/%s/ is classified neither shipped nor internal.\n"+
			"Add it to shippedSubdirs in docs.go (and to the go:embed globs beside it) "+
			"if readers should get it, or to internalSubdirs here with the reason it stays "+
			"in the development repository.", e.Name())
	}
	// The stale-excuse check runs only when the tree HAS internal dirs. The
	// public release tree strips every one of them by design — that is what
	// internalSubdirs means — so their absence there is the projection working,
	// not a stale entry. One internal dir on disk is enough to prove this is
	// the development tree, and then every entry must still point at something.
	anyInternal := false
	for name := range internalSubdirs {
		if onDisk[name] {
			anyInternal = true
			break
		}
	}
	if anyInternal {
		for name := range internalSubdirs {
			if !onDisk[name] {
				t.Errorf("internalSubdirs names docs/%s/, which does not exist — "+
					"a stale excuse hides the next real one", name)
			}
		}
	}
	for name := range shipped {
		if !onDisk[name] {
			t.Errorf("shippedSubdirs names docs/%s/, which does not exist", name)
		}
	}
}

// Every page in docs/ that is meant to ship must reach disk on an install.
//
// This reads the real docs/ directory rather than the embed: asserting the
// installed set against embeddedDocs would only restate the glob back to itself,
// and would have passed just as happily against the hand-maintained map this
// replaced — the map that quietly dropped fifteen pages, web.md included.
// The source tree is the thing the answer has to agree with.
//
// It also closes the gap between shippedSubdirs and the go:embed globs, which
// are two literals that must agree: a directory listed in one and missing from
// the other installs nothing, and the loop in EnsureInstalled would report no
// error doing it.
func TestEveryDocOnDiskIsInstalled(t *testing.T) {
	_, got := installed(t)

	roots := []string{"docs"}
	for _, sub := range shippedSubdirs {
		roots = append(roots, filepath.Join("docs", sub))
	}

	var missing []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s/: %v", root, err)
		}
		found := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(strings.TrimPrefix(root, "docs"), e.Name()))
			rel = strings.TrimPrefix(rel, "/")
			if got[rel] {
				found++
				continue
			}
			missing = append(missing, rel)
		}
		if found == 0 {
			t.Errorf("%s/ installed no pages at all — the go:embed globs in docs.go "+
				"and shippedSubdirs have drifted apart", root)
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
// Top-level docs/*.md count, and so does a page inside a shipped subdirectory
// (docs/design/..., docs/practices/...) — both reach $TERVA_HOME/docs, so both
// are promises. A reference into the internal tier (docs/plans/...,
// docs/architecture/...) is not: those deliberately stay out of the installed
// set, and the pattern is built from shippedSubdirs so it keeps knowing the
// difference. Tests are skipped too: their docs/ paths are invented fixtures
// inside a temp dir, not pointers at terva's own documentation.
func TestDocsReferencedInSourceAreInstalled(t *testing.T) {
	seg := `[A-Za-z0-9_-]+\.md`
	alts := []string{seg}
	for _, sub := range shippedSubdirs {
		alts = append(alts, regexp.QuoteMeta(sub)+`/`+seg)
	}
	ref := regexp.MustCompile(`docs/(` + strings.Join(alts, "|") + `)`)
	_, got := installed(t)

	type site struct{ file, doc string }
	var dangling []site
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored/generated trees are not ours to police, and .claude
			// holds scratch worktrees of this very repo. testsupport owns the
			// rule: this used to keep its own four-name list, which knew
			// nothing about a checkout dropped anywhere else in the tree.
			if testsupport.SkipScanDir(".", path, d) {
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
