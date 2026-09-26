package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stablePrefixes are the terva packages the example may import: the engine and
// the wire, and their subpackages. Until phase 6 of docs/plans/engine-extraction.md
// draws the stable API for real, that is the line.
//
// Only the example's own imports are held here. What the engine reaches in
// turn is held by packages/core's import-boundary baseline, which shrinks as
// the plan lands, so this test does not repeat it.
var stablePrefixes = []string{
	"terva.sh/terva/packages/core",
	"terva.sh/terva/packages/provider",
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
			if !isStable(path) {
				t.Errorf("%s imports %s, which is neither the standard library nor a stable engine or wire package", f, path)
			}
		}
	}
}

func isStable(path string) bool {
	// Standard library paths have no dot in their first element.
	if first, _, _ := strings.Cut(path, "/"); !strings.Contains(first, ".") {
		return true
	}
	for _, p := range stablePrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func TestIsStable(t *testing.T) {
	for path, want := range map[string]bool{
		"fmt":                                 true,
		"encoding/json":                       true,
		"terva.sh/terva/packages/core":        true,
		"terva.sh/terva/packages/core/i18n":   true,
		"terva.sh/terva/packages/provider":    true,
		"terva.sh/terva/packages/providerx":   false,
		"terva.sh/terva/packages/agent/sdk":   false,
		"terva.sh/terva/packages/agent/build": false,
		"github.com/google/uuid":              false,
	} {
		if got := isStable(path); got != want {
			t.Errorf("isStable(%q) = %v, want %v", path, got, want)
		}
	}
}
