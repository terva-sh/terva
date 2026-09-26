package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// An engine's own translator is reached as i18n.In(tr).T(…), and the
// extractor must read those strings as it reads i18n.T(…). An In result kept
// for later hides its strings, so it is reported.
func TestTheExtractorReadsThroughIn(t *testing.T) {
	const src = `package p

func f(tr any) {
	_ = i18n.T("package")
	_ = i18n.In(tr).T("own %d", 1)
	_ = i18n.In(tr).P("prompts.k", "english")
	_ = i18n.In(tr).Errorf("failed")
	_ = i18n.In(tr).String()
	x := i18n.In(tr)
	_ = x.T("hidden")
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fn := i18nFunc(call); fn != "" {
				if lit, ok := litArg(call, 0); ok {
					got = append(got, fn+":"+lit)
				}
			}
		}
		return true
	})
	want := []string{"T:package", "T:own %d", "P:prompts.k", "Errorf:failed"}
	if len(got) != len(want) {
		t.Fatalf("extracted %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extracted %q, want %q", got, want)
			break
		}
	}
	// The String() receiver and the one kept in x are the two unread.
	if n := len(unreadInCalls(file)); n != 2 {
		t.Errorf("%d unread In calls, want 2: the one read by String and the one kept in x", n)
	}
}
