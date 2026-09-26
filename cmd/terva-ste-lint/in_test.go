package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// A model-facing prompt reached through an engine's own translator,
// i18n.In(tr).P(…), is extracted like i18n.P(…). Without this, moving a prompt
// to a per-agent translator would take it out of the language gate.
func TestI18nSelectorReadsThroughIn(t *testing.T) {
	const src = `package p

func f(tr any) {
	_ = i18n.P("a", "one")
	_ = i18n.In(tr).P("b", "two")
	_ = other.In(tr).P("c", "three")
	_ = i18n.Of(tr).P("d", "four")
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if _, ok := i18nSelector(call); ok {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok {
					keys = append(keys, lit.Value)
				}
			}
		}
		return true
	})
	if len(keys) != 2 || keys[0] != `"a"` || keys[1] != `"b"` {
		t.Errorf("read %v, want the i18n.P and i18n.In(tr).P keys only", keys)
	}
}
