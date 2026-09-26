package main

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stablePackages reads the packages .api/packages.txt lists as stable, as
// import paths. Decision 0026 draws that line, and the example may import
// nothing of terva's outside it. A package listed unstable (core/i18n, for
// one) is importable but promises nothing, so the example does not use it.
//
// Only the example's own imports are held here. What the engine reaches in
// turn is held by packages/core's import-boundary baseline, so this test does
// not repeat it.
func stablePackages(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("../../.api/packages.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stable := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == "stable" {
			stable["terva.sh/terva/"+fields[1]] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !stable["terva.sh/terva/packages/core"] || !stable["terva.sh/terva/packages/provider"] {
		t.Fatalf("packages.txt lists %d stable packages, and not the engine and the wire", len(stable))
	}
	return stable
}

// Rule 8 of decision 0021: if the example needs anything else to do something
// basic, the stable API is missing a piece, and the fix is in the API, not an
// entry here.
func TestTheExampleImportsOnlyStablePackages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found; the test is not running in examples/harness")
	}
	stable := stablePackages(t)
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !isStable(stable, path) {
				t.Errorf("%s imports %s, which is neither the standard library nor a package .api/packages.txt lists as stable", f, path)
			}
		}
	}
}

func isStable(stable map[string]bool, path string) bool {
	// Standard library paths have no dot in their first element.
	if first, _, _ := strings.Cut(path, "/"); !strings.Contains(first, ".") {
		return true
	}
	return stable[path]
}

func TestIsStable(t *testing.T) {
	stable := stablePackages(t)
	for path, want := range map[string]bool{
		"fmt":                          true,
		"encoding/json":                true,
		"terva.sh/terva/packages/core": true,
		"terva.sh/terva/packages/core/permission": true,
		"terva.sh/terva/packages/core/i18n":       false,
		"terva.sh/terva/packages/core/exp/foo":    false,
		"terva.sh/terva/packages/provider":        true,
		"terva.sh/terva/packages/providerx":       false,
		"terva.sh/terva/packages/agent/sdk":       true,
		"terva.sh/terva/packages/agent/build":     false,
		"github.com/google/uuid":                  false,
	} {
		if got := isStable(stable, path); got != want {
			t.Errorf("isStable(%q) = %v, want %v", path, got, want)
		}
	}
}
