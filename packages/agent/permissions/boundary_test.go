package permissions

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The direction of the dependency, asserted rather than assumed.
//
// permission_boundary_test.go used to live in package build and hold a
// different line: a written allow-list of the functions permitted to take an
// Args, so the logic underneath them stayed narrow. That guard did its job and
// then made itself obsolete — the logic is a separate package now, and it
// cannot see Args at all.
//
// What replaces it is stronger and smaller. build imports permissions; a back
// edge would be an import cycle, which the compiler already refuses. This test
// exists so the refusal is not the first time anyone hears about the rule, and
// so the reason is written down next to it: build assembles a run, permissions
// decides what that run may do, and a decision that reaches back into the
// assembler is a decision that can see everything.
func TestPermissionsDoesNotImportBuild(t *testing.T) {
	const forbidden = "terva.sh/terva/packages/agent/build"

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("globbed no sources — the guard would pass vacuously")
	}

	fset := token.NewFileSet()
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, im := range f.Imports {
			if strings.Trim(im.Path.Value, `"`) == forbidden {
				t.Errorf("%s imports %s — permissions decides what a run may do; "+
					"it must not be able to see the container that assembles the run. "+
					"Whatever it needs from there belongs on Inputs.", p, forbidden)
			}
		}
	}
}
