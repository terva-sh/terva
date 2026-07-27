package runs

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The reason this package exists is that it can be imported from anywhere, and
// the only thing that keeps that true is what it imports.
//
// A workflow run's artifacts are plain JSON, but the engine that writes them
// needs a JavaScript runtime, and linking sobek in is exactly what the
// `terva_workflows` build tag gates. ctrlproto and workspace are UNTAGGED — so
// the moment anything here reaches back into the engine (or into any package
// that transitively does), every lean build starts carrying a JS interpreter to
// reach a struct definition, silently and with nothing failing.
//
// "Stdlib only" is a coarser rule than "no sobek" and deliberately so: it needs
// no dependency graph to check, holds under every build tag, and cannot be
// satisfied by a hop through an innocent-looking intermediary. If this package
// one day genuinely needs a terva import, that is a decision to make here, in
// this test, rather than one that arrives as a 3 MB binary.
func TestRunsImportsNothingButTheStandardLibrary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			// A stdlib path has no dot in its first element (golang.org/x/…,
			// terva.sh/…, github.com/… all do).
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s — runs must stay importable from untagged code (ctrlproto, workspace) without dragging the JS engine in", name, path)
			}
		}
	}
	// A walk that quietly found nothing would make this vacuous.
	if checked < 2 {
		t.Fatalf("only parsed %d source files; the walk is not seeing the package", checked)
	}
}
