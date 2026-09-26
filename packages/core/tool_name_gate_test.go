package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Decision 0004: the engine never names a concrete tool. A comparison against
// a tool's name ties the engine to one host's registry, and a host with a
// different tool under that name inherits behavior meant for another. A tool
// that needs special handling says so through an interface it implements (see
// LedgerArgsRenderer), which is the change TKT-01M35WJZK made to the ledger's
// `"bash"` branch.
//
// The check is syntactic. It flags every ==, !=, and switch case that sets a
// string literal against an expression that reads as a tool name: one whose
// final identifier is `name`, `tool`, `toolName`, or `Name` (a field or a
// method call). A comparison with the empty string is a presence check and
// passes. Anything it flags that is not a tool name can be renamed.
func TestTheEngineComparesNoToolNameToALiteral(t *testing.T) {
	nameish := regexp.MustCompile(`^(?i:name|tool|toolname)$`)
	isNameish := func(e ast.Expr) bool {
		if c, ok := e.(*ast.CallExpr); ok && len(c.Args) == 0 {
			e = c.Fun
		}
		switch x := e.(type) {
		case *ast.Ident:
			return nameish.MatchString(x.Name)
		case *ast.SelectorExpr:
			return nameish.MatchString(x.Sel.Name)
		}
		return false
	}
	isString := func(e ast.Expr) bool {
		// The empty string names no tool; comparing to it is a presence check.
		l, ok := e.(*ast.BasicLit)
		return ok && l.Kind == token.STRING && l.Value != `""` && l.Value != "``"
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		flag := func(n ast.Node) {
			t.Errorf("%s compares a tool name to a literal; a tool with special handling implements an interface instead", fset.Position(n.Pos()))
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op == token.EQL || x.Op == token.NEQ {
					if (isNameish(x.X) && isString(x.Y)) || (isNameish(x.Y) && isString(x.X)) {
						flag(x)
					}
				}
			case *ast.SwitchStmt:
				if x.Tag == nil || !isNameish(x.Tag) {
					return true
				}
				for _, s := range x.Body.List {
					for _, e := range s.(*ast.CaseClause).List {
						if isString(e) {
							flag(e)
						}
					}
				}
			}
			return true
		})
	}
}
