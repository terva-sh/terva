package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// s.args is the daemon's answer to "what is this session, right now" — and the
// dangerous thing about it is that it is read far from where it is written.
//
// rebuildTools re-resolves the entire tool set from it on five different
// triggers (extension reload, MCP toggle, approval switch, plan mode, trust
// flip), and a fresh Resolve mints fresh tool INSTANCES carrying whatever that
// resolve produced. So any live change the session makes has two halves: apply
// it to the running objects, and record it in args so the next resolve
// reproduces it. Do only the first and the change works — until the next
// rebuild, which silently puts the launch value back.
//
// That is not hypothetical. A mid-session model switch moved the agent, the
// terva_status identity and the host-routed dispatch tools, but not args; the
// next rebuild re-minted terva_status and swarm_spawn from the LAUNCH model, so
// sub-agents ran at a provider the user had switched away from. The bug needed
// two events to appear, which is why it survived the review that built the
// swap.
//
// This test is the list. Every field a live verb writes into s.args has to be
// declared here with what it is FOR — which forces the question "and does the
// rebuild need to see it?" to be answered out loud, once, at the point someone
// adds one.
//
// A stale entry fails too: an allowance nobody uses is a claim about the code
// that has stopped being true.
var sessionMayMutateArgs = map[string]string{
	"Approval": "the approval mode is a live settings switch, and it rides into buildToolRegistry and the merges — plan mode withholds mutating tools from the rebuilt view",
	"Cast":     "the --play cast is edited live from the cast pane; a rebuild re-derives actor_spawn's cast skin from it",

	// The user's own identity, which the per-turn tail renders.
	"As":           "the user persona's name, changed live from the settings pane",
	"UserGender":   "same, and it reaches the model only through a re-resolved per-turn tail",
	"UserPronouns": "same",

	// The model identity. See wsSession.setModel for what a rebuild does with
	// these if they are left behind.
	"Provider": "a mid-session model switch moves the provider; injectExtraTools stamps the host-routed dispatch tools with the resolve's provider, and terva_status reports it",
	"Model":    "the other half of that identity — a rebuild that re-resolves the launch model routes every later sub-agent at it",
	"APIKey":   "cleared when a switch rebuilds the client: a launch-time key pins the endpoint the session has just left",
	"BaseURL":  "cleared for the same reason, and reported to the model by terva_status when it is not",
}

// argsFieldsAssigned returns every X in an `<expr>.args.X = ...` assignment
// across the package's non-test sources, mapped to the files that write it.
// Compound assignment and ++/-- count too: they are writes.
func argsFieldsAssigned(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := map[string][]string{}
	record := func(lhs ast.Expr, path string) {
		outer, ok := lhs.(*ast.SelectorExpr)
		if !ok {
			return
		}
		inner, ok := outer.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "args" {
			return
		}
		field := outer.Sel.Name
		for _, seen := range out[field] {
			if seen == path {
				return
			}
		}
		out[field] = append(out[field], path)
	}
	scanned := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			switch st := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range st.Lhs {
					record(lhs, path)
				}
			case *ast.IncDecStmt:
				record(st.X, path)
			}
			return true
		})
	}
	// A glob that quietly matched nothing would make every assertion vacuous.
	if scanned < 10 {
		t.Fatalf("scanned only %d non-test files; the glob is not seeing the package", scanned)
	}
	return out
}

func TestEveryLiveArgsFieldIsDeclared(t *testing.T) {
	assigned := argsFieldsAssigned(t)
	if len(assigned) == 0 {
		t.Fatal("no `.args.X =` writes found at all — the scan has stopped seeing them, " +
			"and this guard would pass for the rest of its life")
	}

	var undeclared []string
	for field, files := range assigned {
		if _, ok := sessionMayMutateArgs[field]; !ok {
			undeclared = append(undeclared, field+" ("+strings.Join(files, ", ")+")")
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("these Args fields are written live but not declared in sessionMayMutateArgs.\n"+
			"Add each with what it is for — and check the OTHER half while you are there: "+
			"rebuildTools re-resolves from these args, so anything the change also applied "+
			"to a live tool instance gets re-minted from whatever is recorded here:\n  %s",
			strings.Join(undeclared, "\n  "))
	}
}

func TestNoStaleLiveArgsDeclarations(t *testing.T) {
	assigned := argsFieldsAssigned(t)
	var stale []string
	for field := range sessionMayMutateArgs {
		if _, ok := assigned[field]; !ok {
			stale = append(stale, field)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("declared in sessionMayMutateArgs but no longer written live — delete "+
			"these entries: %v", stale)
	}
}
