package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func boolPtr(b bool) *bool { return &b }

// Temperature round-trips through models.json into Model.Temperature; an
// out-of-range value is dropped with a warning (the registry-driven scalar
// override + loader validation).
func TestUserModelTemperature(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"anthropic":{"models":[
		{"id":"claude-warm","temperature":0.7},
		{"id":"claude-bad","temperature":5}
	]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := LoadUserModelsWithWarnings(path)

	var warm, bad *float32
	for _, o := range overrides {
		switch o.Model.ID {
		case "claude-warm":
			warm = o.Model.Temperature
		case "claude-bad":
			bad = o.Model.Temperature
		}
	}
	if warm == nil || *warm != float32(0.7) {
		t.Errorf("temperature 0.7 not loaded: %v", warm)
	}
	if bad != nil {
		t.Errorf("out-of-range temperature should be dropped, got %v", *bad)
	}
	foundWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "temperature") && strings.Contains(w, "out of range") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected an out-of-range temperature warning, got %v", warnings)
	}
}

// applyUserOverrides merges a per-model temperature onto the base catalog
// entry, and a nil override must not clobber a base value.
func TestApplyUserOverridesTemperature(t *testing.T) {
	temp := float32(0.5)
	base := []Model{{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000}}
	merged := applyUserOverrides(base, []UserOverride{
		{Model: Model{Provider: "anthropic", ID: "claude-x", Temperature: &temp}},
	})
	if merged[0].Temperature == nil || *merged[0].Temperature != 0.5 {
		t.Errorf("temperature not merged onto base model: %+v", merged[0])
	}

	base2 := []Model{{Provider: "anthropic", ID: "claude-y", Temperature: &temp}}
	merged2 := applyUserOverrides(base2, []UserOverride{
		{Model: Model{Provider: "anthropic", ID: "claude-y", ContextWindow: 100}},
	})
	if merged2[0].Temperature == nil || *merged2[0].Temperature != 0.5 {
		t.Errorf("a nil override temperature must not clear the base value: %+v", merged2[0])
	}
}

// DefaultReasoning round-trips through models.json into
// Model.DefaultReasoning (the registry-driven scalar override + loader copy).
func TestUserModelDefaultReasoning(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"kimi":{"models":[
		{"id":"k3","defaultReasoning":"off"}
	]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var got string
	found := false
	for _, o := range overrides {
		if o.Model.ID == "k3" {
			got = o.Model.DefaultReasoning
			found = true
		}
	}
	if !found {
		t.Fatal("k3 override not loaded")
	}
	if got != "off" {
		t.Errorf("defaultReasoning = %q, want off", got)
	}
}

// applyUserOverrides merges a per-model defaultReasoning onto the base catalog
// entry (a user "off" overriding a catalog "high"), and a nil override must not
// clobber the base value.
func TestApplyUserOverridesDefaultReasoning(t *testing.T) {
	base := []Model{{Provider: "kimi", ID: "k3", DefaultReasoning: "high"}}
	merged := applyUserOverrides(base, []UserOverride{
		{Model: Model{Provider: "kimi", ID: "k3", DefaultReasoning: "off"}},
	})
	if merged[0].DefaultReasoning != "off" {
		t.Errorf("defaultReasoning override not merged: %+v", merged[0])
	}

	base2 := []Model{{Provider: "kimi", ID: "k3", DefaultReasoning: "high"}}
	merged2 := applyUserOverrides(base2, []UserOverride{
		{Model: Model{Provider: "kimi", ID: "k3", ContextWindow: 100}},
	})
	if merged2[0].DefaultReasoning != "high" {
		t.Errorf("a nil override must not clear the base defaultReasoning: %+v", merged2[0])
	}
}

// TestUpsertInsertReplacePreserve: upsert inserts a new entry, replaces
// an existing one by id, and never disturbs other providers' entries.
func TestUpsertInsertReplacePreserve(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")

	// Seed an unrelated entry that must survive every later write.
	if err := UpsertUserModel(path, "openai", UserModel{ID: "gpt-x", ContextWindow: 1000}); err != nil {
		t.Fatal(err)
	}
	// Insert.
	if err := UpsertUserModel(path, "anthropic", UserModel{ID: "claude-x", MaxTokens: 64000}); err != nil {
		t.Fatal(err)
	}
	// Replace the same id (different value).
	if err := UpsertUserModel(path, "anthropic", UserModel{ID: "claude-x", MaxTokens: 99000, BaseURL: "http://local"}); err != nil {
		t.Fatal(err)
	}

	f, err := ReadUserModelsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(f.Providers["anthropic"].Models); got != 1 {
		t.Fatalf("anthropic should have 1 entry (replaced, not appended), got %d", got)
	}
	got := f.Providers["anthropic"].Models[0]
	if got.MaxTokens != 99000 || got.BaseURL != "http://local" {
		t.Errorf("replace lost fields: %+v", got)
	}
	if len(f.Providers["openai"].Models) != 1 || f.Providers["openai"].Models[0].ID != "gpt-x" {
		t.Errorf("unrelated provider entry was disturbed: %+v", f.Providers["openai"])
	}
}

// TestRemovePrunesEmptyProvider: removing a provider's only model drops
// the whole provider block, and a no-match remove reports false.
func TestRemovePrunesEmptyProvider(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	if err := UpsertUserModel(path, "anthropic", UserModel{ID: "claude-x", MaxTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err := UpsertUserModel(path, "openai", UserModel{ID: "gpt-x", MaxTokens: 1}); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveUserModel(path, "anthropic", "claude-x")
	if err != nil || !removed {
		t.Fatalf("expected removed=true, got removed=%v err=%v", removed, err)
	}
	f, _ := ReadUserModelsFile(path)
	if _, ok := f.Providers["anthropic"]; ok {
		t.Error("emptied provider block should have been pruned")
	}
	if _, ok := f.Providers["openai"]; !ok {
		t.Error("other provider must survive the remove")
	}

	// No-match remove is a no-op that reports false (not an error).
	removed, err = RemoveUserModel(path, "anthropic", "nope")
	if err != nil || removed {
		t.Errorf("no-match remove: want (false,nil), got (%v,%v)", removed, err)
	}
}

// TestWriteRoundTripsThroughLoader: an upserted entry comes back through
// the real catalog loader as a UserOverride with the expected fields.
func TestWriteRoundTripsThroughLoader(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	um := UserModel{
		ID:            "claude-x",
		ContextWindow: 200000,
		MaxTokens:     64000,
		BaseURL:       "http://local:1234",
		Reasoning:     boolPtr(false),
		Capabilities:  map[string]bool{"image-input": false},
	}
	if err := UpsertUserModel(path, "anthropic", um); err != nil {
		t.Fatal(err)
	}

	overrides, warnings := LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(overrides) != 1 {
		t.Fatalf("want 1 override, got %d", len(overrides))
	}
	o := overrides[0]
	if o.Model.Provider != "anthropic" || o.Model.ID != "claude-x" {
		t.Errorf("identity wrong: %+v", o.Model)
	}
	if o.Model.ContextWindow != 200000 || o.Model.MaxOutput != 64000 {
		t.Errorf("sizes wrong: ctx=%d max=%d", o.Model.ContextWindow, o.Model.MaxOutput)
	}
	if o.Model.BaseURL != "http://local:1234" {
		t.Errorf("baseURL wrong: %q", o.Model.BaseURL)
	}
	if !o.ReasoningSet || o.Model.Reasoning {
		t.Errorf("reasoning override not honored: set=%v val=%v", o.ReasoningSet, o.Model.Reasoning)
	}
	if o.Model.Has(CapImageInput) {
		t.Error("image-input:false override not honored")
	}
}

// TestFindUserModel returns the raw entry (distinguishing set from
// unset) and reports absence cleanly.
func TestFindUserModel(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	if err := UpsertUserModel(path, "anthropic", UserModel{ID: "claude-x", BaseURL: "http://local"}); err != nil {
		t.Fatal(err)
	}
	um, ok, err := FindUserModel(path, "anthropic", "claude-x")
	if err != nil || !ok {
		t.Fatalf("expected found, got ok=%v err=%v", ok, err)
	}
	if um.BaseURL != "http://local" || um.ContextWindow != 0 {
		t.Errorf("raw entry wrong (ctx should be 0/unset): %+v", um)
	}
	if _, ok, _ := FindUserModel(path, "anthropic", "missing"); ok {
		t.Error("missing id should report not found")
	}
	// Missing file: not found, no error.
	if _, ok, err := FindUserModel(filepath.Join(testsupport.TempDir(t), "none.json"), "anthropic", "x"); ok || err != nil {
		t.Errorf("missing file: want (false,nil), got (%v,%v)", ok, err)
	}
}

// TestReadMalformedIsError guards the never-clobber invariant: a file
// we can't parse is surfaced as an error, not silently treated as empty.
func TestReadMalformedIsError(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUserModelsFile(path); err == nil {
		t.Error("malformed file should be an error so a rewrite can't clobber it")
	}
}
