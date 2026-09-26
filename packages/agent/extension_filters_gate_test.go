package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// extensionGateHosts is how many functions build a tool gate over an extension
// manager: print, json and bot mode in cli.go, rpc, swarm, acp and the
// workspace. A walk that finds fewer has broken, not shrunk.
const extensionGateHosts = 7

// The extension turn and message intercepts are a component passed to
// NewAgent, no longer a block every host copied onto the agent's fields. The
// cost of that move is a new way to lose them: a host that builds its gate
// over extMgr but forgets build.ExtensionFilters gets the tool ladder and none
// of the intercepts, and nothing else fails.
//
// So the agent constructor that receives a gate built over a manager must
// also receive build.ExtensionFilters over that manager, as a sibling
// argument. The gate may be built inline or bound to a variable first. The
// filters may not be computed and then dropped, or handed to some other call. A nil manager, as
// the SDK passes, has no intercepts to lose.
func TestEveryExtensionGateCarriesItsFilters(t *testing.T) {
	found := 0
	const root = "."
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, fn := range functionBodies(file) {
			for _, g := range gatesIn(fn) {
				found++
				if !g.filtered {
					t.Errorf("%s builds a tool gate over %s, and the agent constructor that receives it has no build.ExtensionFilters(…, %s) beside it, so its extensions cannot stop a turn or rewrite a message",
						fset.Position(g.pos), g.manager, g.manager)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < extensionGateHosts {
		t.Errorf("found %d tool gates built over an extension manager, want at least %d; the pattern has gone stale", found, extensionGateHosts)
	}
}

// functionBodies returns the body of every function declared in file. A
// function literal is part of the declaration that holds it.
func functionBodies(file *ast.File) []*ast.BlockStmt {
	var bodies []*ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			bodies = append(bodies, fn.Body)
		}
	}
	return bodies
}

// agentConstructors are the calls a host builds its agent with: the
// Resolved methods, and cli.go's factory variable over them.
var agentConstructors = map[string]bool{"NewAgent": true, "NewAgentWithFreshTasks": true, "newAgent": true}

// gate is one build.BuildToolGate call over a manager, and whether an agent
// constructor that receives it also receives the manager's filters.
type gate struct {
	manager  string
	pos      token.Pos
	filtered bool
}

// gatesIn finds every gate built in body. A gate reaches a call either inline,
// as build.BuildToolGate(…) in the argument list, or through a variable the
// function bound it to.
func gatesIn(body *ast.BlockStmt) []*gate {
	var gates []*gate
	bound := map[string]*gate{} // variable name → the gate assigned to it
	inline := map[*ast.CallExpr]*gate{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if m, ok := managerArg(call, "BuildToolGate", 2); ok {
			g := &gate{manager: m, pos: call.Pos()}
			gates = append(gates, g)
			inline[call] = g
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range s.Rhs {
				if c, ok := rhs.(*ast.CallExpr); ok && inline[c] != nil && i < len(s.Lhs) {
					if id, ok := s.Lhs[i].(*ast.Ident); ok {
						bound[id.Name] = inline[c]
					}
				}
			}
		case *ast.ValueSpec:
			for i, v := range s.Values {
				if c, ok := v.(*ast.CallExpr); ok && inline[c] != nil && i < len(s.Names) {
					bound[s.Names[i].Name] = inline[c]
				}
			}
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !agentConstructors[calleeName(call)] {
			return true
		}
		var received []*gate
		filters := map[string]bool{}
		for _, arg := range call.Args {
			switch a := arg.(type) {
			case *ast.CallExpr:
				if g := inline[a]; g != nil {
					received = append(received, g)
				}
				if m, ok := managerArg(a, "ExtensionFilters", 1); ok {
					filters[m] = true
				}
			case *ast.Ident:
				if g := bound[a.Name]; g != nil {
					received = append(received, g)
				}
			}
		}
		for _, g := range received {
			if filters[g.manager] {
				g.filtered = true
			}
		}
		return true
	})
	return gates
}

// calleeName is the name call invokes: f for f(…), and m for x.m(…).
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// managerArg reports the source text of argument i of call when call is
// build.<name>, such as extMgr or s.extMgr. A literal nil names no manager.
func managerArg(call *ast.CallExpr, name string, i int) (string, bool) {
	if len(call.Args) <= i {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "build" {
		return "", false
	}
	if id, ok := call.Args[i].(*ast.Ident); ok && id.Name == "nil" {
		return "", false
	}
	return types.ExprString(call.Args[i]), true
}
