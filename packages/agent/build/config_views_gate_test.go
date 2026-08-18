package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// repairFields are the only settings Resolve may touch through the USER layer.
//
// Both are config REPAIR: when the saved provider has no credential, or the
// saved model has left the catalogue, Resolve rewrites the user's config so the
// problem stops recurring. Those writes must land on the user layer — persisting
// a project's provider into someone's home config would make a repo's choice
// follow them everywhere.
//
// cfg.Model is also READ at the repair site, to decide whether the value being
// repaired is still the one this run resolved from. That is a read OF the user
// layer FOR the user layer, which is the point.
var repairFields = map[string]string{
	"Provider": "reset to a credentialed provider when the saved one has no key",
	"Model":    "repair a saved model that has left the active catalogue",
}

// Resolve reads settings through the layered view and writes through the user
// layer. Nothing reads a setting off cfg to decide behaviour.
//
// Held by a scan rather than by the comment that used to claim it, because that
// comment was false when written and stayed false: Provider, Model, LazyToolsOn,
// EngineFeatures and Escalation all read eff.Config while Reasoning, Temperature,
// Lore, Endpoints, NativeOutput, ReasoningSummary and ShowReasoning read cfg.
// The two views are identical for everything config.ResolveConfig does not
// layer, so both spellings worked and the choice was arbitrary at every site.
//
// The trap that makes it worth a gate: ResolveConfig layers seven fields today.
// The moment an eighth is added — a per-repo thinking level, say — every read
// still on the user layer silently ignores the project's value. There is no
// error and no warning, and build's own tests seed the USER config, so the whole
// suite stays green while the feature does nothing.
func TestResolveReadsSettingsThroughTheLayeredView(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "build.go", nil, 0)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Resolve" && fd.Recv == nil {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("no top-level Resolve in build.go — the gate is scanning the wrong thing")
	}

	seen := map[string]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "cfg" {
			return true
		}
		seen[sel.Sel.Name]++
		if _, allowed := repairFields[sel.Sel.Name]; allowed {
			return true
		}
		t.Errorf("build.go:%d reads cfg.%s — the USER layer — to decide behaviour.\n"+
			"  Use eff.Config.%s. cfg is the writable user layer and exists here only for the "+
			"config-repair writes; a setting read off it ignores any project value the moment "+
			"config.ResolveConfig starts layering that field, with no error and no failing test.",
			fset.Position(sel.Pos()).Line, sel.Sel.Name, sel.Sel.Name)
		return true
	})

	// Vacuity floor: cfg is still used for the repairs, so a scan that found
	// NOTHING is a broken walk rather than a clean function.
	if len(seen) == 0 {
		t.Fatal("the scan found no cfg.X selectors at all; the walk is broken and a pass here proves nothing")
	}

	// A repair field that has stopped being used is a stale licence: it would
	// silently re-permit a behaviour read of that name later.
	for name, why := range repairFields {
		if seen[name] == 0 {
			var have []string
			for k := range seen {
				have = append(have, k)
			}
			sort.Strings(have)
			t.Errorf("repairFields permits cfg.%s (%s) but Resolve no longer touches it; "+
				"drop the entry. Present: %v", name, why, have)
		}
	}
}

// The other half of the rule, and the reason cfg exists at all: the repair
// writes must NOT move to the layered view.
//
// Writing a repair through eff.Config would persist whatever the project layer
// contributed into the user's own config — a repo's provider choice following
// them to every other checkout. This asserts the repair sites are assignments
// to cfg, so "unify everything on eff.Config" cannot be over-applied later by
// someone tidying up.
func TestResolveRepairsWriteToTheUserLayer(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "build.go", nil, 0)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}

	// 🪤 Resolve the ROOT of the selector chain, not its immediate X.
	//
	// `eff.Config.Model = x` parses as SelectorExpr{X: SelectorExpr{X: eff}},
	// so a check that type-asserts sel.X to *ast.Ident skips it silently. The
	// first version of this gate did exactly that and passed under a mutation
	// that moved a repair write onto the layered view — the precise thing it
	// exists to forbid. Found by mutating it, not by reading it.
	rootIdent := func(e ast.Expr) string {
		for {
			switch v := e.(type) {
			case *ast.Ident:
				return v.Name
			case *ast.SelectorExpr:
				e = v.X
			case *ast.IndexExpr:
				e = v.X
			default:
				return ""
			}
		}
	}

	assigned := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			switch rootIdent(sel) {
			case "cfg":
				assigned[sel.Sel.Name]++
			case "eff":
				t.Errorf("build.go:%d assigns through eff — a repair must write the USER layer (cfg), "+
					"or a project's value is persisted into the user's own config and follows them "+
					"to every other checkout", fset.Position(sel.Pos()).Line)
			}
		}
		return true
	})

	// Counted, not just present. Model is assigned at TWO repair sites; a
	// boolean "is it assigned anywhere" stayed true when one of them moved,
	// which is how the mutation above went unnoticed a second way.
	wantAssignments := map[string]int{"Provider": 1, "Model": 2}
	for name := range repairFields {
		want := wantAssignments[name]
		if assigned[name] != want {
			t.Errorf("cfg.%s is assigned %d time(s), want %d. If a repair site moved or was added, "+
				"update this count deliberately — a loose check here is what let a repair write "+
				"slip onto the layered view.", name, assigned[name], want)
		}
	}
}
