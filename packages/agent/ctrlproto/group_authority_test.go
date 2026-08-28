package ctrlproto

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The group is THE unit of authority gating: hello.go negotiates groups, and
// serve.go refuses a verb whose group the peer did not negotiate. GroupAuth is
// separate from GroupControl precisely so that "may switch models and edit lore"
// does not silently become "may REPLACE YOUR ANTHROPIC TOKEN", and GroupSecrets
// is one rung above that again.
//
// What guarded that assignment before this file: TestEveryMethodHasAGroup, which
// calls caseIdents() — and caseIdents flattens the case clauses of ALL FOUR arms
// into a single set. It proves a verb appears SOMEWHERE in Group(). It cannot
// tell GroupSession from GroupControl. A verb typed into the wrong 51-identifier
// case list changes which peers may call it, and every test stays green.
//
// The three tests here close that in layers, and they are deliberately different
// in kind:
//
//  1. TestEveryVerbResolvesToAKnownGroup — runtime, airtight, no expectation
//     needed. Every constant must yield one of the six real groups. This is the
//     strong form of the presence check: caseIdents asks whether an identifier
//     was typed inside some case clause, this asks what Group() actually
//     RETURNS, which is what serve.go acts on.
//
//  2. TestGatedVerbsMatchTheirController — a structural INVARIANT, derived, and
//     the one that catches a privilege change. The dispatch table already
//     records which controller answers each verb. A verb served by
//     AuthController belongs to GroupAuth by construction: the controller and
//     the group exist for the same reason and are advertised together. So the
//     expectation is read out of dispatch_table.go rather than written down,
//     and a secrets verb quietly refiled into `control` fails here.
//
//  3. TestMethodGroupsArePinned — a golden. It does NOT prove correctness; it
//     pins the current assignment so that CHANGING one is a reviewable diff
//     instead of an invisible edit inside a long comma-separated case list.
//     That is a weaker claim than (2) and it is stated weakly on purpose: a
//     golden regenerated with -update blesses whatever it is given, so its value
//     is that a reclassification cannot pass unnoticed, not that it is right.
//
// Only (2) can say a verb is in the WRONG group. It covers the three gated
// groups, which is where a mistake is a security bug rather than an
// inconvenience. Sorting conversation from session from control needs judgement
// no mechanism has, and (3) is what puts that judgement in front of a reviewer.

// gatedControllers maps an optional controller to the group a verb it serves
// must belong to. These three groups are each advertised only by a carrier that
// implements the matching controller (see hello.go), so the pairing is a fact
// about the protocol, not a convention.
var gatedControllers = map[string]Group{
	"AuthController":    GroupAuth,
	"SecretsController": GroupSecrets,
	"ReplayController":  GroupReplay,
}

// methodValuesByName maps each Method constant NAME to its wire string.
func methodValuesByName(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	out := map[string]string{}
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
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(bl.Value); err == nil {
					out[n.Name] = v
				}
			}
		}
	}
	if len(out) < 90 {
		t.Fatalf("found only %d Method constants; the parse is not seeing them", len(out))
	}
	return out
}

// verbControllers maps each verb's wire string to the receiver TYPE its
// dispatch entry binds — "AuthController", "WorkspaceService", and so on.
func verbControllers(t *testing.T) map[string]string {
	t.Helper()
	values := methodValuesByName(t)

	f, err := parser.ParseFile(token.NewFileSet(), "dispatch_table.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatch_table.go: %v", err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		verb, ok := values[key.Name]
		if !ok {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, a := range call.Args {
			lit, ok := a.(*ast.FuncLit)
			if !ok {
				continue
			}
			if lit.Type.Params == nil || len(lit.Type.Params.List) == 0 {
				continue
			}
			if id, ok := lit.Type.Params.List[0].Type.(*ast.Ident); ok {
				out[verb] = id.Name
			}
			break
		}
		return true
	})
	if len(out) < 100 {
		t.Fatalf("resolved only %d verb->controller entries; the parse is not seeing them", len(out))
	}
	return out
}

// knownGroups is every group the protocol defines.
var knownGroups = map[Group]bool{
	GroupConversation: true,
	GroupSession:      true,
	GroupControl:      true,
	GroupReplay:       true,
	GroupAuth:         true,
	GroupSecrets:      true,
}

// TestEveryVerbResolvesToAKnownGroup is the airtight form of the presence
// check. Group() returning "" is not a routing error anywhere — serve.go simply
// finds no negotiated group to match, so the verb becomes unreachable rather
// than loudly broken.
func TestEveryVerbResolvesToAKnownGroup(t *testing.T) {
	values := methodValuesByName(t)
	for name, verb := range values {
		g := Method(verb).Group()
		if g == "" {
			t.Errorf("%s (%q) resolves to no group. serve.go will never match it "+
				"against a negotiated group, so no peer can call it.", name, verb)
			continue
		}
		if !knownGroups[g] {
			t.Errorf("%s (%q) resolves to group %q, which the protocol does not define", name, verb, g)
		}
	}
}

// TestGatedVerbsMatchTheirController is the one test here that can say a verb
// sits in the WRONG group. Both directions are checked: a gated controller's
// verb must be in its group, and that group must contain nothing else.
func TestGatedVerbsMatchTheirController(t *testing.T) {
	controllers := verbControllers(t)
	values := methodValuesByName(t)

	// Forward: a verb served by a gated controller must be in its group.
	checked := 0
	for verb, ctrl := range controllers {
		want, gated := gatedControllers[ctrl]
		if !gated {
			continue
		}
		checked++
		if got := Method(verb).Group(); got != want {
			t.Errorf("%q is served by %s but sits in group %q, not %q.\n"+
				"A carrier advertises that group only when it implements that controller, "+
				"so this verb is either unreachable or reachable at the wrong authority.",
				verb, ctrl, got, want)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d gated verbs were checked; the dispatch parse is not seeing them", checked)
	}

	// Reverse: a gated group must contain only verbs its controller serves.
	// This is the direction that catches a high-authority group being used as a
	// parking space for something unrelated.
	gatedGroups := map[Group]string{}
	for ctrl, g := range gatedControllers {
		gatedGroups[g] = ctrl
	}
	for name, verb := range values {
		g := Method(verb).Group()
		wantCtrl, isGated := gatedGroups[g]
		if !isGated {
			continue
		}
		got, dispatched := controllers[verb]
		if !dispatched {
			// Served outside the dispatch table (serveState answers it) — that
			// is a different contract and not this test's business.
			continue
		}
		if got != wantCtrl {
			t.Errorf("%s (%q) is in group %q, which is gated on %s, but its handler binds %s.\n"+
				"Either the group is wrong or the verb belongs on the other controller.",
				name, verb, g, wantCtrl, got)
		}
	}
}

// TestMethodGroupsArePinned pins every verb's group to testdata/method_groups.txt.
//
// Regenerate deliberately with `go test ./packages/agent/ctrlproto -run
// TestMethodGroupsArePinned -update`, and read the diff: a line that moves
// between groups is a change to who may call that verb.
func TestMethodGroupsArePinned(t *testing.T) {
	values := methodValuesByName(t)

	lines := make([]string, 0, len(values))
	for _, verb := range values {
		lines = append(lines, fmt.Sprintf("%s\t%s", verb, Method(verb).Group()))
	}
	sort.Strings(lines)
	got := []byte(strings.Join(lines, "\n") + "\n")

	if len(lines) < 140 {
		t.Fatalf("only %d verbs pinned; the parse is not seeing the constant block", len(lines))
	}

	path := filepath.Join("testdata", "method_groups.txt")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("method group assignments changed.\n"+
			"Every line here is an authority decision: the group decides which peers "+
			"may call the verb.\nIf the change is intended, rerun with -update and let "+
			"the diff be reviewed.\n\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
