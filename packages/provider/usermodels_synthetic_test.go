package provider

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// writeModelsJSONBody drops a models.json holding body and returns its path.
// The neighbouring writeModelsJSON builds one entry from parts; this one takes
// the whole document, because these cases care about what is NOT in it.
func writeModelsJSONBody(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A model the operator invented reaches the merged catalog marked Synthetic.
// This is the whole point of the flag: the entry is the only thing holding the
// model up, so removing it deletes the model rather than restoring a default.
func TestSyntheticMarksAModelThatExistsOnlyInModelsJSON(t *testing.T) {
	path := writeModelsJSONBody(t, `{"providers":{"openai":{"models":[
		{"id":"gpt-6-astra","contextWindow":400000,"maxTokens":128000}
	]}}}`)
	overrides, warnings := LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	merged := applyUserOverrides(nil, overrides)
	if len(merged) != 1 {
		t.Fatalf("got %d models, want the invented one", len(merged))
	}
	if !merged[0].Synthetic {
		t.Error("a model with no catalog row and no discovery is not marked Synthetic; " +
			"a picker cannot then tell the operator that removing the entry deletes it")
	}
}

// The other branch. An entry that tweaks a row some lower layer supplied is an
// override, not an invention, and removing it restores the catalog value.
func TestOverrideOnACatalogRowIsNotSynthetic(t *testing.T) {
	path := writeModelsJSONBody(t, `{"providers":{"anthropic":{"models":[
		{"id":"claude-x","contextWindow":123000}
	]}}}`)
	overrides, _ := LoadUserModelsWithWarnings(path)

	base := []Model{{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000, Source: "catalog"}}
	merged := applyUserOverrides(base, overrides)
	if len(merged) != 1 {
		t.Fatalf("got %d models, want the catalog row merged in place", len(merged))
	}
	if merged[0].Synthetic {
		t.Error("a tweak on a catalog row is marked Synthetic; " +
			"a picker would offer to delete a model it cannot delete")
	}
	if merged[0].ContextWindow != 123000 {
		t.Errorf("precondition: the override did not apply, got %d", merged[0].ContextWindow)
	}
}

// 🪤 The reason Synthetic is a separate field and not derived from Source.
//
// Both cases read Source=="user" by the time a picker sees them, by two
// different routes: the loader stamps every entry it builds, and the merge
// branch stamps the row it lands on. So Source cannot separate them. Anyone who
// later decides Synthetic is redundant and reads Source instead collapses
// "custom" and "overridden" into one word, and the destructive action goes with
// it.
//
// Both sides go through the loader, because that is the only production path
// into the user layer and it is what supplies Source on the invented side. A
// hand-built UserOverride carries no Source at all, so building one here would
// prove the wrong thing.
func TestSourceCannotTellAnInventedModelFromATweakedOne(t *testing.T) {
	inventedPath := writeModelsJSONBody(t, `{"providers":{"openai":{"models":[
		{"id":"gpt-6-astra","contextWindow":400000}
	]}}}`)
	inventedOver, _ := LoadUserModelsWithWarnings(inventedPath)
	invented := applyUserOverrides(nil, inventedOver)

	tweakedPath := writeModelsJSONBody(t, `{"providers":{"anthropic":{"models":[
		{"id":"claude-x","contextWindow":123000}
	]}}}`)
	tweakedOver, _ := LoadUserModelsWithWarnings(tweakedPath)
	tweaked := applyUserOverrides(
		[]Model{{Provider: "anthropic", ID: "claude-x", Source: "catalog"}},
		tweakedOver,
	)

	if invented[0].Source != "user" || tweaked[0].Source != "user" {
		t.Fatalf("precondition changed: Source is %q and %q, both were \"user\"",
			invented[0].Source, tweaked[0].Source)
	}
	if invented[0].Synthetic == tweaked[0].Synthetic {
		t.Error("Synthetic does not separate the two cases that Source cannot; " +
			"the field has no reason to exist if this passes")
	}
}

// Synthetic is not sticky. When terva ships a catalog row for a model the
// operator added by hand, or /v1/models discovers it, the same entry becomes a
// plain override on the next merge. Without this the picker would keep offering
// to "delete" a model that now has a real row underneath it.
func TestSyntheticClearsOnceTheCatalogCatchesUp(t *testing.T) {
	overrides := []UserOverride{
		{Model: Model{Provider: "openai", ID: "gpt-6-astra", ContextWindow: 400000}},
	}

	before := applyUserOverrides(nil, overrides)
	if !before[0].Synthetic {
		t.Fatal("precondition: the model should start out synthetic")
	}

	// The same entry, now with a shipped catalog row underneath it.
	after := applyUserOverrides(
		[]Model{{Provider: "openai", ID: "gpt-6-astra", ContextWindow: 400000, Source: "catalog"}},
		overrides,
	)
	if after[0].Synthetic {
		t.Error("Synthetic survived the arrival of a catalog row; " +
			"the entry is a tweak now, and removing it restores a default")
	}
}
