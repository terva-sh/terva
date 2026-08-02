package raati

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every swarm.SpawnRequest this package builds must set SharedTree, because
// every agent this package spawns is a tool-less panelist: it reads evidence
// and returns a ballot, and a private git checkout is something it cannot use.
//
// Under --swarm-worktrees each seat was leasing one anyway, then releasing it —
// and release KEEPS the worktree, which is right for a coding sub-agent whose
// work you want to review and pointless for a vote. One convening left one
// worktree per seat, forever. On the machine where this was found, 36 of 44
// unclaimed worktrees were raati seats.
//
// This SCANS the package rather than listing the two spawn sites, so a third
// one added later fails here instead of quietly filling someone's disk again.
// It is written empty by design: the first run was the audit, and it found the
// round-two reseat that the round-one fix had missed.
func TestEverySpawnRunsInTheSharedTree(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	found := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SpawnRequest" {
				return true
			}
			found++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "SharedTree" {
					if val, ok := kv.Value.(*ast.Ident); ok && val.Name == "true" {
						return true
					}
					t.Errorf("%s:%d: SharedTree is set to something other than true",
						name, fset.Position(kv.Pos()).Line)
					return true
				}
			}
			t.Errorf("%s:%d: swarm.SpawnRequest without SharedTree — a tool-less panelist would lease a git worktree it cannot use",
				name, fset.Position(lit.Pos()).Line)
			return true
		})
	}

	if found < 2 {
		t.Fatalf("scanned %d SpawnRequest literals; raati builds one per round and the walk is not seeing them", found)
	}
}
