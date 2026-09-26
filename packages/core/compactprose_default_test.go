package core

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/core/compactprose"
	"terva.sh/terva/packages/provider"
)

// A host that supplies no policy gets the neutral default, and it is enough to
// keep a long conversation under the window: the automatic compaction fires,
// sends the neutral text, and leaves a checkpoint that names no product.
func TestTheNeutralDefaultCompactsALongSession(t *testing.T) {
	client := &midTurnFakeClient{}
	a := newTestAgent(client, "claude-sonnet-4-5", "", Registry{})
	seedSmallTranscript(a, 40)
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000}) // 95% of 200k

	_, ran, err := a.CompactIfDue(context.Background(), CompactAfterTurn, nil)
	if !ran || err != nil {
		t.Fatalf("the default did not compact a full session: ran %v, err %v", ran, err)
	}
	req := client.reqs[len(client.reqs)-1]
	if req.System != compactprose.System {
		t.Errorf("the summary request did not carry the neutral system prompt: %q", req.System)
	}
	var sent strings.Builder
	sent.WriteString(req.System)
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				sent.WriteString(tb.Text)
			}
		}
	}
	if !strings.Contains(sent.String(), "## Goal") {
		t.Error("the summary request did not ask for the checkpoint format")
	}
	if strings.Contains(strings.ToLower(sent.String()), "terva") {
		t.Error("the neutral text names a product")
	}
	if n := len(a.Messages()); n >= 40 {
		t.Errorf("the transcript still has %d messages", n)
	}
}

// Rule 4 of decision 0021: the model-facing text of compaction and the
// context-pressure note is the host's, and the engine keeps only the neutral
// default in compactprose. Any catalog key for that text in the engine's own
// files means it has crept back.
func TestNoCompactionProseInTheEngine(t *testing.T) {
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
		for _, key := range compactionPromptKeys(file) {
			t.Errorf("%s carries compaction prose (i18n P key %q); it belongs to the host's policy or to compactprose", f, key)
		}
	}
}

// compactionPromptKeys returns the compaction and context-pressure keys file
// renders through i18n.P or through an agent's translator, i18n.In(…).P, with
// any argument to In.
func compactionPromptKeys(file *ast.File) []string {
	var keys []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "P" {
			return true
		}
		x := sel.X
		if in, ok := x.(*ast.CallExpr); ok {
			inSel, ok := in.Fun.(*ast.SelectorExpr)
			if !ok || inSel.Sel.Name != "In" {
				return true
			}
			x = inSel.X
		}
		if pkg, ok := x.(*ast.Ident); !ok || pkg.Name != "i18n" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err == nil && (strings.HasPrefix(key, "compact.") || strings.HasPrefix(key, "context.pressure")) {
			keys = append(keys, key)
		}
		return true
	})
	return keys
}

// The guard reads every spelling an engine file could use, a nested call
// inside In included.
func TestTheCompactionProseGuardReadsEverySpelling(t *testing.T) {
	const src = `package p

func f(a any) {
	_ = i18n.P("compact.one", "x")
	_ = i18n.In(nil).P("compact.two", "x")
	_ = i18n.In(a.Translator()).P("context.pressure.three", "x")
	_ = i18n.In(pick(a.Translator(), nil)).P("compact.four", "x")
	_ = i18n.P("other.key", "x")
	_ = fmt.P("compact.not_i18n", "x")
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(compactionPromptKeys(file), ",")
	if want := "compact.one,compact.two,context.pressure.three,compact.four"; got != want {
		t.Errorf("the guard found %s, want %s", got, want)
	}
}

// The neutral failure note knows nothing about the tool, so it must not claim
// the effect is absent or partial: a call can fail after its effect landed.
func TestTheNeutralFailureNoteClaimsNothingAboutTheEffect(t *testing.T) {
	note := compactprose.LedgerFailed("any_tool")
	if !strings.Contains(note, "all of it may exist") {
		t.Errorf("the note rules out that the whole effect exists: %q", note)
	}
}
