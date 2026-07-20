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

// singletonCaseIdents collects only the identifiers that appear ALONE in a case
// clause — `case MethodX:`, never `case MethodX, MethodY:`.
//
// This is the distinction that matters for dispatch. serve.go groups related
// verbs behind one outer clause (`case MethodA, MethodB, MethodC:`) that resolves
// the controller once, then re-switches on f.Method to bind each verb's own
// params. caseIdents cannot tell those two layers apart: it harvests the outer
// group list too, so a verb named ONLY there — and therefore actually handled by
// the inner `default:` with another verb's params bound — still counts as
// dispatched. That is exactly how personas.delete came to be dispatched as
// PersonasEdit, binding {name} as a write and overwriting the persona with an
// empty one (see the backstop comment in serve.go).
//
// Requiring a singleton case means every verb must be named on an arm of its
// own, so the group list alone can no longer satisfy the check.
func singletonCaseIdents(t *testing.T, file, fn string) map[string]bool {
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
			if cc, ok := n.(*ast.CaseClause); ok && len(cc.List) == 1 {
				if id, ok := cc.List[0].(*ast.Ident); ok {
					out[id.Name] = true
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no singleton case identifiers found in %s:%s — was it renamed?", file, fn)
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

// Every Method must be dispatched on an arm of ITS OWN, not merely named in a
// group's outer case list. TestEveryMethodIsDispatched above cannot see the
// difference, and the gap is not theoretical: a verb reached only through a
// group's `default:` runs another verb's handler with its params bound to the
// wrong struct — silently, and destructively for delete/set/bind verbs. See
// singletonCaseIdents.
func TestEveryMethodIsDispatchedExplicitly(t *testing.T) {
	all := methodConstants(t)
	if got := methodsMissingFrom(all, singletonCaseIdents(t, "serve.go", "handle")); len(got) > 0 {
		t.Fatalf("methods never named on a case arm of their own in serve.go's dispatch "+
			"(a group's outer case list does not count — such a verb is handled by the "+
			"group's default with another verb's params): %v", got)
	}
}
