package slug

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The fence that replaced package scope.
//
// These functions were unexported in package build, and being unexported was
// the whole guarantee: a stem is an internal filing decision, and nothing
// outside build could mint one. Moving them here to share them with package
// persona had to export them, which throws that guarantee away — unless
// something else holds it.
//
// This is that something. A stem determines where a file lands on disk and
// which built-in a user copy shadows; a caller that mints one is participating
// in the library layout whether it means to or not. So importing this package
// is a decision recorded here, next to the store the importer owns, rather than
// a thing that quietly becomes true.
//
// A stale entry fails too, so a package that stops importing cannot leave a
// claim behind.
var permittedImporters = map[string]string{
	"terva.sh/terva/packages/agent/build":   "owns the card, card-history, card-model, group, user-persona and world stores",
	"terva.sh/terva/packages/agent/persona": "owns the persona library under $TERVA_HOME/personas — the store whose stems must agree with the card library's",
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

const selfPath = "terva.sh/terva/packages/agent/slug"

// importersOfSlug returns every first-party package that imports this one,
// mapped to the file that proved it.
func importersOfSlug(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	found := map[string]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// The interesting failure mode of a repo-wide walk is not what it
		// finds but what it walks into — nested checkouts under .claude
		// hold branches predating any rule this asserts.
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // not our business to fail on somebody else's parse error
		}
		for _, im := range f.Imports {
			if strings.Trim(im.Path.Value, `"`) != selfPath {
				continue
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			pkg := "terva.sh/terva/" + filepath.ToSlash(rel)
			if _, seen := found[pkg]; !seen {
				r, _ := filepath.Rel(root, path)
				found[pkg] = filepath.ToSlash(r)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestOnlyPermittedPackagesImportSlug(t *testing.T) {
	for pkg, file := range importersOfSlug(t) {
		if _, ok := permittedImporters[pkg]; !ok {
			t.Errorf("%s imports package slug (%s) but is not in permittedImporters.\n"+
				"A stem decides where a file lands and which built-in a user copy shadows. "+
				"If this package owns a library of its own, add it with the store it owns; "+
				"if it just needs a name normalized, it wants its own helper, not this one.",
				pkg, file)
		}
	}
}

func TestNoStalePermittedImporters(t *testing.T) {
	live := importersOfSlug(t)
	var stale []string
	for pkg := range permittedImporters {
		if _, ok := live[pkg]; !ok {
			stale = append(stale, pkg)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("permittedImporters names %s, which no longer imports package slug — "+
			"drop the entry so the list keeps meaning what it says", strings.Join(stale, ", "))
	}
}
