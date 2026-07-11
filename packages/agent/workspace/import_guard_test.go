package workspace

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestWorkspaceDoesNotImportModes guards the dependency direction: the
// daemon/control-plane workspace layer must not import the frontend
// packages/agent/modes package. The Raati summarizer/clerk previously reached
// into modes.RunPrint for a throwaway headless turn; that primitive now lives
// in the neutral packages/agent/run. The check parses this package's source
// directly (not the resolved import graph) so it holds under every build tag.
func TestWorkspaceDoesNotImportModes(t *testing.T) {
	assertPackageDoesNotImport(t, "terva.sh/terva/packages/agent/modes",
		"workspace is daemon/control-plane state and must not import the frontend modes package; use packages/agent/run for a headless turn")
}

// assertPackageDoesNotImport fails if any non-test .go file in the current
// package directory imports forbidden.
func assertPackageDoesNotImport(t *testing.T, forbidden, why string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == forbidden {
				t.Errorf("%s imports %s — %s", name, forbidden, why)
			}
		}
	}
}
