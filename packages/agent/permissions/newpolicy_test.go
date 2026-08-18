package permissions

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// A PermissionPolicy has seven fields and every one of them is load-bearing.
// acp_mode.go hand-built a second policy and set four, so an ACP session that
// tightened its mode with session/set_mode evaluated tools differently from
// every other host.
//
// This asserts on the FIELDS, by reflection, so a field added to the struct
// later is covered by having been added — a test naming today's seven could not
// fail when an eighth arrives unset.
func TestNewPolicyFillsEveryField(t *testing.T) {
	p := NewPolicy(core.ApprovalYolo, nil)

	v := reflect.ValueOf(*p)
	typ := v.Type()
	if typ.NumField() < 7 {
		t.Fatalf("PermissionPolicy has %d fields; the reflection is not reading the struct", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "Rules" {
			continue // legitimately empty for a rule-less policy
		}
		if v.Field(i).IsZero() {
			t.Errorf("NewPolicy leaves %s at its zero value. Every field here decides how a tool "+
				"is judged once the mode tightens; an unset one is a silent difference in behaviour "+
				"between hosts.", name)
		}
	}
}

// The two the ACP twin actually omitted, named so a regression says which
// behaviour went away rather than just "a field is zero".
func TestTheTwoFieldsTheACPTwinOmitted(t *testing.T) {
	p := NewPolicy(core.ApprovalPlan, nil)

	// Interactive: ask_user_question is permitted in every mode and never
	// prompts. Without it, plan mode — which an ACP client reaches through
	// session/set_mode — blocks a question that can never be approved.
	if !p.Interactive["ask_user_question"] {
		t.Error("Interactive does not classify ask_user_question: in plan mode the agent cannot " +
			"ask the user anything, and there is no approval that would let it")
	}

	// DecomposeCommand: a compound command is judged per command, so an allow
	// rule scoped to one program cannot clear another on the same line.
	if p.DecomposeCommand == nil {
		t.Fatal("DecomposeCommand is nil: `git diff && rm -rf /` is judged as ONE unit, so a rule " +
			"allowing `git *` covers the rm")
	}
	parts := p.DecomposeCommand("bash", json.RawMessage(`{"command":"git diff && rm -rf /tmp/x"}`))
	if len(parts) != 2 {
		t.Errorf("a two-command line decomposed into %d scope(s), want 2", len(parts))
	}
}

// Nothing outside this package may compose a core.PermissionPolicy literal.
// The defect was a second one built by hand; a hand-written list of known
// builders cannot fail when a third appears.
func TestNobodyElseComposesAPermissionPolicy(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var offenders []string
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		// This package IS the builder.
		if strings.HasPrefix(rel, filepath.Join("packages", "agent", "permissions")) {
			return nil
		}
		scanned++
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil // not our business; the compiler's
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "PermissionPolicy" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "core" {
				offenders = append(offenders, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 200 {
		t.Fatalf("scanned only %d Go files; the walk is broken and this census proves nothing", scanned)
	}
	for _, o := range offenders {
		t.Errorf("%s composes a core.PermissionPolicy literal — use permissions.NewPolicy. "+
			"The last hand-built one set four of seven fields and changed how rules were evaluated.", o)
	}
}
