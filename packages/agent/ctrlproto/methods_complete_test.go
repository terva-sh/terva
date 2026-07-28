package ctrlproto

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Adding a verb means touching three places: the Method constant, Group(), and
// the dispatch table. The compiler catches none of them on its own.
//
// A method missing from Group() gets the empty group, which is not a routing
// error anywhere. One missing from the table answers `unknown method`. Both fail
// at runtime, on a client with no idea why its call vanished.
//
// The Group() check reads the SOURCE, because Group() is a switch and a switch
// can only be asked what it covers by parsing it. The dispatch check does not:
// the table is data, so it is a lookup. Both enroll a new constant automatically.

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

// Every Method must be dispatched.
//
// This used to PARSE serve.go's switch with go/ast, because a hand-written switch
// has no other way to be asked what it covers, and a missing case silently
// answered `unknown method` to a client with no idea why its call vanished.
//
// Dispatch is a map now, so the question is a lookup. Worth noting what that
// change RETIRED rather than moved: its companion test,
// TestEveryMethodIsDispatchedExplicitly, existed because the switch let several
// verbs share one outer `case MethodA, MethodB, MethodC:` and re-switch inside —
// a verb named only in that outer list fell to the inner `default:` and ran
// another verb's handler with its params bound to the wrong struct. It shipped
// seven times; personas.delete ran as PersonasEdit and overwrote the persona with
// an empty one. A map key cannot be shared and there is no inner default, so the
// bug is now impossible rather than guarded, and the test that guarded it is gone
// with the shape that made it necessary.
func TestEveryMethodIsDispatched(t *testing.T) {
	special := map[Method]string{
		MethodSubscribe:   "serveState owns the event pump; no service call to table",
		MethodUnsubscribe: "cancels the pump subscribe registered",
	}
	var missing []string
	for name, m := range methodValues(t) {
		if _, ok := dispatch[m]; ok {
			continue
		}
		if _, ok := special[m]; ok {
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (%q)", name, m))
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("methods with no entry in the dispatch table (dispatch_table.go) — add "+
			"one, or a `special` entry here saying why serveState answers it:\n  %s",
			strings.Join(missing, "\n  "))
	}
	// The reverse direction: an entry for a verb that no longer exists would sit
	// there forever, since a map lookup never complains about an unused key.
	all := map[Method]bool{}
	for _, m := range methodValues(t) {
		all[m] = true
	}
	var stale []string
	for m := range dispatch {
		if !all[m] {
			stale = append(stale, string(m))
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("dispatch table has entries for verbs that are not Method constants: %v", stale)
	}
}

// A verb serveState answers itself must NOT also be in the table: the table would
// never be consulted for it (handle returns first), so the entry would be dead
// code that reads as live dispatch.
func TestServeStateVerbsAreNotAlsoTabled(t *testing.T) {
	for _, m := range []Method{MethodSubscribe, MethodUnsubscribe} {
		if _, ok := dispatch[m]; ok {
			t.Errorf("%s is answered by serveState AND has a dispatch entry; the entry is unreachable", m)
		}
	}
}
