package worktree

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// HostRoots must be the only place the pair is spelled.
//
// Three production sites built it from the same two literals — the tool
// registry, the swarm's worktree carrier, and the web Worktrees panel's session
// env — plus a test fixture that set Root and omitted LegacyRoot while driving
// production removeAvailableWorktrees. They agreed, which is the only reason
// nothing had broken.
//
// What a disagreement costs: Root is where the registry lives, LegacyRoot is
// what triggers the one-time migration off the retired extension's data dir. A
// caller naming a different pair addresses a DIFFERENT registry for the same
// repo, and resolveRepo succeeds against an empty one. worktree_create writes
// under the new root while the panel reads the old; worktree_release cannot
// find a claim the swarm carrier made; the swarm's checkouts and their branches
// leak with no surface able to see them. No error anywhere.
//
// Scanned over the whole tree rather than a list of the three known sites,
// because a list cannot fail when a FOURTH is added — which is exactly how the
// hand-rolled test fixture came to exist.
//
// What counts as an offence is narrow, and deliberately so. Only a
// filepath.Join whose LAST element is a root's own name and whose base names
// the terva home constructs the root. A join that reaches INSIDE one
// (repo.go's <legacyRoot>/<key>/worktrees, a fixture's
// <tmp>/worktrees/<name>) is ordinary path work, and a gate that failed on it
// would be one somebody has to switch off.
func TestNobodySpellsTheWorktreeRootsByHand(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	rootName, legacyPath := HostRoots("")
	tail := map[string]bool{
		strings.Trim(filepath.ToSlash(rootName), "/"): true, // "worktrees"
		filepath.Base(legacyPath):                     true, // "git-worktree"
	}

	scanned := 0
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(repoRoot, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		if rel == filepath.Join("packages", "agent", "worktree", "hostenv.go") {
			return nil // this file DEFINES the pair
		}
		scanned++
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Join" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "filepath" {
				return true
			}
			last, ok := call.Args[len(call.Args)-1].(*ast.BasicLit)
			if !ok || last.Kind != token.STRING || !tail[strings.Trim(last.Value, `"`)] {
				return true
			}
			// Rooted at the terva home? Then this IS the root, not a path inside it.
			var base strings.Builder
			_ = printExpr(&base, call.Args[0])
			if !strings.Contains(base.String(), "TervaHome") && !strings.Contains(base.String(), "TERVA_HOME") {
				return true
			}
			t.Errorf("%s:%d builds a worktree root by hand from %s. Use worktree.HostEnv / "+
				"worktree.HostRoots: Root and LegacyRoot must be named together, and an env missing "+
				"LegacyRoot addresses a different registry for the same repo — then finds it empty "+
				"rather than erroring.", rel, fset.Position(call.Pos()).Line, base.String())
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d Go files; the walk is broken and a pass here proves nothing", scanned)
	}
	if len(tail) != 2 {
		t.Fatalf("derived %d root names from HostRoots, want 2; the scan is looking for the wrong thing", len(tail))
	}
}

// printExpr renders an expression back to something readable enough to test for
// a TervaHome reference.
func printExpr(b *strings.Builder, e ast.Expr) error {
	switch v := e.(type) {
	case *ast.Ident:
		b.WriteString(v.Name)
	case *ast.BasicLit:
		b.WriteString(v.Value)
	case *ast.SelectorExpr:
		_ = printExpr(b, v.X)
		b.WriteString("." + v.Sel.Name)
	case *ast.CallExpr:
		_ = printExpr(b, v.Fun)
		b.WriteString("(")
		for i, a := range v.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			_ = printExpr(b, a)
		}
		b.WriteString(")")
	}
	return nil
}

// The pair itself: both halves present, both under the home, and distinct. A
// constructor returning the same path twice would satisfy every caller and
// still address one registry for two purposes.
func TestHostRootsNamesTwoDistinctPlacesUnderTheHome(t *testing.T) {
	root, legacy := HostRoots("/home/x/.terva")
	if root == legacy {
		t.Fatal("Root and LegacyRoot are the same path")
	}
	for _, p := range []string{root, legacy} {
		if !strings.HasPrefix(p, "/home/x/.terva"+string(filepath.Separator)) {
			t.Errorf("%q is not under the given home", p)
		}
	}
}

// HostEnv must fill BOTH roots. Omitting LegacyRoot is the specific mistake the
// hand-rolled fixture made, and it is silent.
func TestHostEnvCarriesBothRoots(t *testing.T) {
	env := HostEnv("/home/x/.terva", "/repo", "sess-1")
	if env.Root == "" {
		t.Error("HostEnv left Root empty")
	}
	if env.LegacyRoot == "" {
		t.Error("HostEnv left LegacyRoot empty — the migration off the retired extension's " +
			"data dir never runs, so a repo registered only there reads as having no worktrees")
	}
	if env.CWD != "/repo" || env.SessionID != "sess-1" {
		t.Errorf("HostEnv lost its caller's cwd/session: %+v", env)
	}
}
