package modes

import (
	"go/ast"
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

// InteractiveConfig is the TUI's injection seam: the host fills it in, and the
// loop nil-checks a field to decide whether it has that capability. That works
// only while a nil field MEANS something. It stopped meaning anything.
//
// The legacy direct driver was removed and the carrier became the only TUI
// backend, but the fields the old driver filled in stayed — 28 of them, nil
// under every frontend that exists. Each still had a live `if i.cfg.X != nil`
// whose other arm no caller could reach, so the struct read as a capability
// matrix while being, in that part, a list of things nothing does.
//
// This gate keeps the seam honest: every field must be set by a PRODUCTION
// caller, or carry a written reason here. A field only tests set is not a
// capability — it is a fixture, and saying so is the point.
//
// Deliberately source-parsed rather than reflective: the question is not what
// a field's zero value is at runtime, it is whether any caller anywhere ever
// writes it. Only the source answers that.

// unsetByDesign names a field no production caller sets, with the reason it is
// still declared. Empty, and meant to stay that way — an entry is a claim that
// a field earns its place while nothing fills it, which is a real thing (a seam
// staged ahead of the host that will use it) but never the default.
var unsetByDesign = map[string]string{}

// modesFieldSources are the trees a production caller could live in: the modes
// package itself and the composition root above it.
var interactiveConfigCallers = []string{".", "..", "../workspace"}

func interactiveConfigFields(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "interactive.go", nil, 0)
	if err != nil {
		t.Fatalf("parse interactive.go: %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "InteractiveConfig" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, nm := range fld.Names {
				out = append(out, nm.Name)
			}
		}
		return false
	})
	if len(out) < 40 {
		t.Fatalf("found only %d InteractiveConfig fields; the parse is not seeing them", len(out))
	}
	return out
}

// setByAProductionCaller returns the field names written by any non-test
// InteractiveConfig composite literal.
func setByAProductionCaller(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	seen := false
	for _, root := range interactiveConfigCallers {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				// A nested checkout under packages/agent holds somebody
				// else's InteractiveConfig literals, on somebody else's branch.
				if testsupport.SkipScanDir(root, path, fs.FileInfoToDirEntry(info)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok || !isInteractiveConfigLit(cl.Type) {
					return true
				}
				seen = true
				for _, elt := range cl.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							set[id.Name] = true
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if !seen {
		t.Fatal("found no production InteractiveConfig literal — the walk is not reaching the composition root, " +
			"so an empty result would wrongly condemn every field")
	}
	return set
}

func isInteractiveConfigLit(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "InteractiveConfig"
	case *ast.SelectorExpr:
		return t.Sel.Name == "InteractiveConfig"
	}
	return false
}

func TestEveryInteractiveConfigFieldHasAProductionCaller(t *testing.T) {
	set := setByAProductionCaller(t)

	var dead, stale []string
	for _, name := range interactiveConfigFields(t) {
		_, excused := unsetByDesign[name]
		switch {
		case !set[name] && !excused:
			dead = append(dead, name)
		case set[name] && excused:
			stale = append(stale, name)
		}
	}
	sort.Strings(dead)
	sort.Strings(stale)

	if len(dead) > 0 {
		t.Errorf("InteractiveConfig fields no production caller sets — they are nil under every frontend, "+
			"so the loop's `if i.cfg.X != nil` can never take the non-nil arm. Remove the field and the "+
			"unreachable branch, or add a reasoned entry to unsetByDesign:\n  %s",
			strings.Join(dead, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("on unsetByDesign but now set by a production caller — delete the entries: %s",
			strings.Join(stale, ", "))
	}
}
