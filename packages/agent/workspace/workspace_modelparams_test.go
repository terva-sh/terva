package workspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// These settings — context window, desired context window, max tokens — existed
// only in a TUI dialog. packages/provider described them properly all along; the
// single caller was a terminal overlay, so no other frontend could touch one.

func modelParamsWorkspace(t *testing.T) *Workspace {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	return &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
}

// aModel picks a real catalog model, so the test describes what terva actually
// ships rather than a fixture that cannot drift with it.
func aModel(t *testing.T) provider.Model {
	t.Helper()
	m, err := provider.FindModel("anthropic", "")
	if err != nil {
		models := provider.ModelsForProvider("anthropic")
		if len(models) == 0 {
			t.Skip("no anthropic models in the catalog")
		}
		return models[0]
	}
	return m
}

// The descriptor must carry what a form needs to render itself: a label, a kind,
// and — the one that decides whether an empty box reads as "inherit" or as
// "zero" — the default this model would take.
func TestModelParamsDescribesTheSettingsAndTheirDefaults(t *testing.T) {
	w := modelParamsWorkspace(t)
	m := aModel(t)

	v, err := w.ModelParams(context.Background(), ctrlproto.ModelParamsParams{Provider: m.Provider, Model: m.ID})
	if err != nil {
		t.Fatalf("ModelParams: %v", err)
	}
	if v.HasOverride {
		t.Error("a fresh TERVA_HOME has no models.json, so nothing can be overridden yet")
	}

	byKey := map[string]ctrlproto.ModelParamSpec{}
	for _, p := range v.Params {
		byKey[p.Key] = p
	}
	// The settings the operator actually asked for.
	for _, want := range []string{"contextWindow", "desiredContextWindow", "maxTokens", "temperature", "baseUrl"} {
		if _, ok := byKey[want]; !ok {
			t.Errorf("the descriptor omits %q, so no client can offer it", want)
		}
	}
	if byKey["contextWindow"].Kind != "int" {
		t.Errorf("contextWindow kind = %q, want int", byKey["contextWindow"].Kind)
	}
	if byKey["temperature"].Kind != "float" {
		t.Errorf("temperature kind = %q, want float", byKey["temperature"].Kind)
	}
	if byKey["contextWindow"].Default == "" {
		t.Error("no default for the context window: an empty box would then read as zero rather than as 'whatever terva knows'")
	}
	// Nothing is pinned yet, so nothing may claim to be.
	if byKey["contextWindow"].Value != "" {
		t.Errorf("contextWindow value = %q, want empty: models.json holds nothing", byKey["contextWindow"].Value)
	}
	// desiredContextWindow is the one nobody can guess from its name.
	if byKey["desiredContextWindow"].Help == "" {
		t.Error("desiredContextWindow has no help; it is not the model's ceiling and the name does not say so")
	}
}

// A set lands in models.json and comes back on the next read — the round trip the
// TUI has always had.
func TestModelParamsSetPersistsAndReadsBack(t *testing.T) {
	w := modelParamsWorkspace(t)
	m := aModel(t)
	ctx := context.Background()
	ref := ctrlproto.ModelParamsParams{Provider: m.Provider, Model: m.ID}

	err := w.ModelParamsSet(ctx, ctrlproto.ModelParamsSetParams{
		Provider: m.Provider, Model: m.ID,
		Values: map[string]string{"desiredContextWindow": "60000", "maxTokens": "8192"},
	})
	if err != nil {
		t.Fatalf("ModelParamsSet: %v", err)
	}

	v, err := w.ModelParams(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasOverride {
		t.Error("HasOverride is false after a write; nothing would offer to reset it")
	}
	byKey := map[string]string{}
	for _, p := range v.Params {
		byKey[p.Key] = p.Value
	}
	if byKey["desiredContextWindow"] != "60000" || byKey["maxTokens"] != "8192" {
		t.Errorf("read back %+v, want the values just written", byKey)
	}

	// And it is really on disk, in the file the TUI and the CLI read.
	raw, readErr := os.ReadFile(config.UserModelsPath())
	if readErr != nil {
		t.Fatalf("models.json was not written: %v", readErr)
	}
	if !json.Valid(raw) {
		t.Fatal("models.json is not valid JSON")
	}
}

// A blank CLEARS an override rather than writing a zero. Without this an operator
// could never undo one field — and a stored 0 is a very different instruction from
// "inherit whatever terva knows".
func TestAnEmptyValueClearsTheOverrideRatherThanWritingZero(t *testing.T) {
	w := modelParamsWorkspace(t)
	m := aModel(t)
	ctx := context.Background()
	ref := ctrlproto.ModelParamsParams{Provider: m.Provider, Model: m.ID}

	if err := w.ModelParamsSet(ctx, ctrlproto.ModelParamsSetParams{
		Provider: m.Provider, Model: m.ID,
		Values: map[string]string{"maxTokens": "8192"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.ModelParamsSet(ctx, ctrlproto.ModelParamsSetParams{
		Provider: m.Provider, Model: m.ID,
		Values: map[string]string{"maxTokens": ""},
	}); err != nil {
		t.Fatal(err)
	}

	v, _ := w.ModelParams(ctx, ref)
	for _, p := range v.Params {
		if p.Key == "maxTokens" && p.Value != "" {
			t.Errorf("maxTokens = %q after being cleared, want empty", p.Value)
		}
	}
}

// The daemon parses, not the client. A refusal must name the setting the operator
// typed into, or they are hunting a rejected form with no idea which box did it.
func TestABadValueIsRefusedAndNamesTheSetting(t *testing.T) {
	w := modelParamsWorkspace(t)
	m := aModel(t)

	err := w.ModelParamsSet(context.Background(), ctrlproto.ModelParamsSetParams{
		Provider: m.Provider, Model: m.ID,
		Values: map[string]string{"contextWindow": "not-a-number"},
	})
	if err == nil {
		t.Fatal("a non-numeric context window was accepted")
	}
	if !strings.Contains(err.Error(), "context window") {
		t.Errorf("error %q does not name the setting that was wrong", err)
	}
}

// Reset removes the entry outright — back to what terva ships, not to an entry
// full of blanks that still shadows the catalog.
func TestResetRemovesTheEntry(t *testing.T) {
	w := modelParamsWorkspace(t)
	m := aModel(t)
	ctx := context.Background()
	ref := ctrlproto.ModelParamsParams{Provider: m.Provider, Model: m.ID}

	if err := w.ModelParamsSet(ctx, ctrlproto.ModelParamsSetParams{
		Provider: m.Provider, Model: m.ID,
		Values: map[string]string{"maxTokens": "4096"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.ModelParamsReset(ctx, ref); err != nil {
		t.Fatalf("ModelParamsReset: %v", err)
	}

	v, _ := w.ModelParams(ctx, ref)
	if v.HasOverride {
		t.Error("the models.json entry survived a reset")
	}
}
