package workspace

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// modelsWithReasoning lists models.list rows for a catalog holding one model
// with an OPERATOR's per-model level (DefaultReasoningSet), one with a CATALOG
// default, and one with neither, against the given global.
//
// The two model rows are the same FIELD on opposite sides of the global, which
// is the whole difficulty: read DefaultReasoning without the set-signal and the
// operator's choice and the shipped fallback are indistinguishable.
func modelsWithReasoning(t *testing.T, global string) map[string]ctrlproto.ModelInfo {
	t.Helper()
	seedCreds(t, "")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Reasoning = global
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	provider.SetUserModels([]provider.Model{
		{
			Provider: "workshop", ID: "operator-set", ContextWindow: 4096, Reasoning: true,
			DefaultReasoning: "minimum", DefaultReasoningSet: true,
		},
		{
			Provider: "workshop", ID: "catalog-default", ContextWindow: 4096, Reasoning: true,
			DefaultReasoning: "medium",
		},
		{Provider: "workshop", ID: "says-nothing", ContextWindow: 4096, Reasoning: true},
	})

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	out := map[string]ctrlproto.ModelInfo{}
	for _, m := range res.Models {
		if m.Provider == "workshop" {
			out[m.ID] = m
		}
	}
	if len(out) != 3 {
		t.Fatalf("listed %d workshop models, want the 3 seeded — the fixture is not reaching the picker", len(out))
	}
	return out
}

// TestModelsListCarriesTheResolvedInheritLevel is the whole point of the wire
// change: the client is handed the ANSWER, not the inputs.
//
// Every surface that tried to work it out locally made the same mistake, and
// they could not have done otherwise — the global is workspace state a picker
// has no copy of, and the two model rungs are one field told apart only by a
// signal that was never on the wire. So an operator who set a per-model level
// was told the session would "follow the global setting", naming a value that
// was not deciding anything while the turn ran at theirs.
func TestModelsListCarriesTheResolvedInheritLevel(t *testing.T) {
	got := modelsWithReasoning(t, "high")

	for _, tc := range []struct {
		id    string
		level string
		from  ctrlproto.ReasoningSource
		why   string
	}{
		{"operator-set", "minimum", ctrlproto.ReasoningFromModelOperator,
			"an operator's per-model models.json level outranks the global"},
		{"catalog-default", "high", ctrlproto.ReasoningFromGlobal,
			"a CATALOG default yields to the global, unlike an operator's"},
		{"says-nothing", "high", ctrlproto.ReasoningFromGlobal,
			"with nothing on the model the global decides"},
	} {
		m := got[tc.id]
		if m.InheritReasoning != tc.level || m.InheritReasoningFrom != tc.from {
			t.Errorf("%s inherits (%q, %q), want (%q, %q) — %s",
				tc.id, m.InheritReasoning, m.InheritReasoningFrom, tc.level, tc.from, tc.why)
		}
	}

	// The two model rows carry the SAME raw DefaultReasoning shape, so a client
	// reading that field alone cannot tell them apart. That is why the resolved
	// pair exists, and asserting it here keeps the premise honest.
	if got["operator-set"].DefaultReasoning == "" || got["catalog-default"].DefaultReasoning == "" {
		t.Fatal("both rows should still carry a raw default; without that the case above is not the hard one")
	}
}

// TestWithNoGlobalTheModelsOwnDefaultDecides: the lower half of the chain, where
// a catalog default is the last thing standing and must be NAMED as the model's
// own rather than as a global that is not set.
func TestWithNoGlobalTheModelsOwnDefaultDecides(t *testing.T) {
	got := modelsWithReasoning(t, "")

	if m := got["catalog-default"]; m.InheritReasoning != "medium" || m.InheritReasoningFrom != ctrlproto.ReasoningFromModelCatalog {
		t.Errorf("catalog-default inherits (%q, %q), want (\"medium\", %q)",
			m.InheritReasoning, m.InheritReasoningFrom, ctrlproto.ReasoningFromModelCatalog)
	}
	if m := got["operator-set"]; m.InheritReasoning != "minimum" || m.InheritReasoningFrom != ctrlproto.ReasoningFromModelOperator {
		t.Errorf("operator-set inherits (%q, %q), want (\"minimum\", %q)",
			m.InheritReasoning, m.InheritReasoningFrom, ctrlproto.ReasoningFromModelOperator)
	}
	// Nothing anywhere: empty, and empty on the wire, so a client renders no
	// level rather than a wrong one.
	if m := got["says-nothing"]; m.InheritReasoning != "" || m.InheritReasoningFrom != ctrlproto.ReasoningFromNothing {
		t.Errorf("says-nothing inherits (%q, %q), want both empty", m.InheritReasoning, m.InheritReasoningFrom)
	}
}

// TestEveryReasoningSourceHasAWireName is a census, not a list: it reads
// provider's layers out of the package source, so a layer added there fails
// here until wireReasoningSource learns it.
//
// Without it a new layer falls through the switch to "", which a client reads as
// "nothing is set anywhere" — so the picker would name the model's own default
// while the turn ran at whatever the new layer decided. Silent, and wrong in the
// same direction as the bug this whole change is about.
func TestEveryReasoningSourceHasAWireName(t *testing.T) {
	names := providerReasoningSourceConstants(t)
	if len(names) < 4 {
		t.Fatalf("found %d provider.ReasoningSource constant(s) — the scan is not finding them: %v", len(names), names)
	}
	seen := map[ctrlproto.ReasoningSource]string{}
	for i, name := range names {
		src := provider.ReasoningSource(i)
		wire := wireReasoningSource(src)
		// ReasoningFromNothing is deliberately empty on the wire: absent means
		// absent, and a client needs no arm for it.
		if name != "ReasoningFromNothing" && wire == ctrlproto.ReasoningFromNothing {
			t.Errorf("provider.%s has no wire name, so it reaches a client as \"nothing is set anywhere\" — "+
				"add an arm to wireReasoningSource and a value to ctrlproto.ReasoningSource", name)
			continue
		}
		if prev, dup := seen[wire]; dup && wire != ctrlproto.ReasoningFromNothing {
			t.Errorf("provider.%s and provider.%s both map to %q — a client cannot tell them apart", name, prev, wire)
		}
		seen[wire] = name
	}
}

// providerReasoningSourceConstants returns provider's ReasoningSource constants
// in DECLARATION order, which is iota order — the order the values carry.
func providerReasoningSourceConstants(t *testing.T) []string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	path := filepath.Join(filepath.Dir(self), "..", "..", "provider", "reasoning.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse provider/reasoning.go: %v", err)
	}
	var out []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		// Whole-block: in an iota block only the FIRST spec names the type.
		named := false
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "ReasoningSource" {
					named = true
				}
			}
		}
		if !named {
			continue
		}
		for _, spec := range gd.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, id := range vs.Names {
					out = append(out, id.Name)
				}
			}
		}
	}
	return out
}
