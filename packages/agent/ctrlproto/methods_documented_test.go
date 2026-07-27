package ctrlproto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// docs/controllers.md is the protocol's reference: the place a frontend author
// reads to learn what a verb takes and returns. Nothing tied it to methods.go,
// so it covered roughly half the surface and drifted line by line (its
// workflows.list row still described the pre-liveness status domain months of
// releases after "running" shipped). This test makes the doc part of adding a
// verb: a new Method constant must appear backticked in controllers.md, or
// carry a reason on the gap list below.
//
// The gap list is the honest backlog, visible in one place instead of
// invisible everywhere — the same shape as the hello feature-advertisement
// exempt list. Entries are grouped by why they are undocumented; documenting
// one means deleting its entry, and the reverse check enforces that.

// undocumented is the gap list: a verb served but not yet in controllers.md,
// each with the reason it is not. It is EMPTY, and that is the point — the
// pre-guard backlog of 57 verbs it was seeded with has been written out, so a
// verb missing a doc row now fails the moment it lands rather than joining a
// list nobody reads.
//
// Adding an entry is allowed and sometimes right (a verb shipped behind a flag
// nobody can reach yet, say). Write the real reason, not a placeholder: the
// entry is the record of a decision, and a stale one fails too.
var undocumented = map[string]string{}

// verbStrings returns every Method constant's VALUE ("sessions.list"),
// where methodConstants next door returns their names.
func verbStrings(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	var out []string
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
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					out = append(out, s)
				}
			}
		}
	}
	if len(out) < 90 {
		t.Fatalf("found only %d Method values; the parse is not seeing them", len(out))
	}
	return out
}

func TestEveryMethodIsDocumentedOrOnTheGapList(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/controllers.md")
	if err != nil {
		t.Fatalf("read docs/controllers.md: %v", err)
	}
	text := string(doc)
	var missing, stale []string
	for _, verb := range verbStrings(t) {
		documented := strings.Contains(text, "`"+verb+"`")
		_, excused := undocumented[verb]
		switch {
		case !documented && !excused:
			missing = append(missing, verb)
		case documented && excused:
			stale = append(stale, verb)
		}
	}
	if len(missing) > 0 {
		t.Errorf("verbs served but absent from docs/controllers.md (document them, or add a reasoned entry to the gap list): %s",
			strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("on the gap list but now documented — delete the entries: %s", strings.Join(stale, ", "))
	}
}
