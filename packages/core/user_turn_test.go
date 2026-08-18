package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func userMsg(text string, meta map[string]string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Meta:    meta,
	}
}

// TestIsUserTurnRejectsEveryMachineAuthoredUserMessage names the four, so a
// change to any one of them is a failure with a name on it rather than a silent
// extra row in a picker.
func TestIsUserTurnRejectsEveryMachineAuthoredUserMessage(t *testing.T) {
	if !IsUserTurn(userMsg("what does this do?", nil)) {
		t.Error("a plain typed prompt is not a user turn; every other case here is now vacuous")
	}
	if !IsUserTurn(userMsg("look at this", map[string]string{MetaPreamble: "true"})) {
		t.Error("a prompt carrying a host-assembled preamble is still the user's turn — they typed the rest of it")
	}
	for _, tc := range []struct {
		what string
		msg  provider.Message
	}{
		{"a tool-image mirror", provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: ToolImageMirrorPrefix}},
			Meta:    map[string]string{toolImageMirrorMeta: "true"},
		}},
		{"a legacy tool-image mirror, recognised by its prefix", provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: ToolImageMirrorPrefix}},
		}},
		{"a compaction summary", userMsg("Conversation so far: …", map[string]string{MetaCompaction: "true"})},
		{"a host-injected nudge", userMsg("Please continue.", map[string]string{MetaSynthetic: "true"})},
		{"a /clear divider", provider.Message{Role: provider.RoleUser, Meta: map[string]string{MetaClear: "true"}}},
		{"a crossed /clear divider", provider.Message{Role: provider.RoleUser, Meta: map[string]string{MetaClear: "crossed"}}},
	} {
		if IsUserTurn(tc.msg) {
			t.Errorf("IsUserTurn says %s is something the user said", tc.what)
		}
	}
	if IsUserTurn(provider.Message{Role: provider.RoleAssistant}) {
		t.Error("an assistant message is not a user turn")
	}
}

// metaConstClassification records, for every Meta* constant wire.go declares,
// whether a user message carrying it is still the user's turn.
//
// The point is the CENSUS below, not the list: these four arrived one at a time,
// and both readers of "did the user say this" were written against whichever
// subset existed that week. A new Meta constant now fails this until somebody
// says which of the three it is — which is the moment to remember the picker.
type metaClass int

const (
	metaNotATurn   metaClass = iota // a user message carrying this key is machine-authored
	metaRidesATurn                  // this key can ride a message the user really typed
	metaIsAValue                    // not a key at all — a VALUE for another key
)

var metaConstClassification = map[string]struct {
	class metaClass
	why   string
}{
	"MetaSynthetic":          {metaNotATurn, "the at-close continuation-gate nudge; the host typed it"},
	"MetaCompaction":         {metaNotATurn, "the summary a checkpoint left behind; its own doc says render it as a divider"},
	"MetaClear":              {metaNotATurn, "a client-side divider standing in for a /clear; it has no content at all"},
	"MetaTokensBefore":       {metaRidesATurn, "rides the compaction summary, which MetaCompaction already rejects; alone it says nothing about authorship"},
	"MetaSource":             {metaRidesATurn, "names what produced a message; on a user message that is Stage authorship, which the user did"},
	"MetaActor":              {metaRidesATurn, "names the speaking character on a directed line the user drafted"},
	"MetaPreamble":           {metaRidesATurn, "the FIRST block is host-assembled; the rest is what the user typed"},
	"MetaAttachments":        {metaRidesATurn, "files the user attached to their own prompt"},
	"MetaAttachmentsMissing": {metaRidesATurn, "files that were swept before the prompt went out; still their prompt"},
	"MetaShared":             {metaRidesATurn, "published artefacts hung on a tool result"},
	"MetaDirected":           {metaIsAValue, "a MetaSource VALUE"},
	"MetaRouted":             {metaIsAValue, "a MetaSource VALUE"},
}

// TestEveryMetaConstantIsClassifiedForUserTurns is the census: it reads the
// Meta* constants out of wire.go rather than trusting the table above to be
// current, and then CHECKS the classification against IsUserTurn's behaviour, so
// an entry cannot be wrong in either direction.
func TestEveryMetaConstantIsClassifiedForUserTurns(t *testing.T) {
	declared := metaConstantNames(t)
	if len(declared) < 10 {
		t.Fatalf("found only %d Meta* constant(s) in wire.go — the scan is not finding them: %v", len(declared), declared)
	}
	for _, name := range declared {
		if _, ok := metaConstClassification[name]; !ok {
			t.Errorf("%s is declared in wire.go but metaConstClassification does not say whether a user message carrying it "+
				"is still the user's turn — classify it, and if it is metaNotATurn teach IsUserTurn about it", name)
		}
	}
	have := map[string]bool{}
	for _, n := range declared {
		have[n] = true
	}
	for name, c := range metaConstClassification {
		if !have[name] {
			t.Errorf("metaConstClassification classifies %q (%s), which wire.go no longer declares — drop the entry", name, c.why)
		}
	}

	// And the classification has to match what IsUserTurn actually does.
	for name, c := range metaConstClassification {
		if !have[name] {
			continue
		}
		value, ok := metaConstValue(t, name)
		if !ok {
			continue
		}
		switch c.class {
		case metaNotATurn:
			if IsUserTurn(userMsg("text", map[string]string{value: "true"})) {
				t.Errorf("%s is classified metaNotATurn (%s) but IsUserTurn accepts a user message carrying it", name, c.why)
			}
		case metaRidesATurn:
			if !IsUserTurn(userMsg("text", map[string]string{value: "true"})) {
				t.Errorf("%s is classified metaRidesATurn (%s) but IsUserTurn rejects a user message carrying it", name, c.why)
			}
		case metaIsAValue:
			// Nothing to assert: it is never a key.
		}
	}
}

// metaConstantNames returns every Meta*-prefixed constant declared in wire.go.
func metaConstantNames(t *testing.T) []string {
	t.Helper()
	var out []string
	for name := range parseWireMetaConsts(t) {
		out = append(out, name)
	}
	return out
}

func metaConstValue(t *testing.T, name string) (string, bool) {
	t.Helper()
	v, ok := parseWireMetaConsts(t)[name]
	return v, ok
}

func parseWireMetaConsts(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse wire.go: %v", err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "Meta") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[id.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	return out
}
