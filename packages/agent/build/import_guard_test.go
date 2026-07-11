package build

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestBuildDoesNotImportTUI guards the dependency direction: packages/agent/build
// owns CLI argument parsing and validation, not presentation. Its only tui user
// was PrintHelp, which moved to the composition root (packages/agent). The check
// parses this package's source directly so it holds under every build tag.
func TestBuildDoesNotImportTUI(t *testing.T) {
	const forbidden = "terva.sh/terva/packages/tui"
	const why = "build owns arg parsing/validation, not rendering; keep help/presentation in the composition package (PrintHelp lives in packages/agent)"

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
