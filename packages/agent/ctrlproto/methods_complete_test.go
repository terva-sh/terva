package ctrlproto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// Adding a verb means touching four places: the Method constant, Group(),
// serve.go's hand-written dispatch switch, and every WorkspaceService
// implementation. The compiler catches only the last one.
//
// A method missing from Group() gets the empty group. One missing from the
// dispatch switch falls through to `unknown method`. Both fail at runtime, on
// a client with no idea why its call vanished.
//
// These read the source rather than a hand-kept list, so a new constant
// enrolls itself.

// methodConstants returns every `MethodX Method = "..."` declared in methods.go.
func methodConstants(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Method" {
				continue
			}
			for _, n := range vs.Names {
				out[n.Name] = true
			}
		}
	}
	// A parser that quietly finds nothing would make both tests vacuous.
	if len(out) < 10 {
		t.Fatalf("found only %d Method constants; the parse is not seeing them", len(out))
	}
	return out
}

// caseIdents collects the identifiers named in the case clauses of fn's
// switch statements.
func caseIdents(t *testing.T, file, fn string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if cc, ok := n.(*ast.CaseClause); ok {
				for _, e := range cc.List {
					if id, ok := e.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no case identifiers found in %s:%s — was it renamed?", file, fn)
	}
	return out
}

func methodsMissingFrom(all, have map[string]bool) []string {
	var out []string
	for name := range all {
		if !have[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Every Method must resolve to a group; Group() returns "" for one it has
// never heard of, and an empty group is not a routing error anywhere.
func TestEveryMethodHasAGroup(t *testing.T) {
	all := methodConstants(t)
	if got := methodsMissingFrom(all, caseIdents(t, "methods.go", "Group")); len(got) > 0 {
		t.Fatalf("methods missing from Method.Group(): %v", got)
	}
}

// Every Method must be dispatched. serve.go's switch is hand-written, and a
// missing case silently answers `unknown method`.
func TestEveryMethodIsDispatched(t *testing.T) {
	all := methodConstants(t)
	if got := methodsMissingFrom(all, caseIdents(t, "serve.go", "handle")); len(got) > 0 {
		t.Fatalf("methods missing from serve.go's dispatch switch: %v", got)
	}
}
