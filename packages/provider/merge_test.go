package provider

import "testing"

// bakedIDs returns up to n baked model ids for a provider (test helper).
func bakedIDs(provider string, n int) []string {
	var out []string
	for _, m := range Catalog {
		if m.Provider == provider {
			out = append(out, m.ID)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

func hasModel(models []Model, provider, id string) bool {
	for _, m := range models {
		if m.Provider == provider && m.ID == id {
			return true
		}
	}
	return false
}

// For an authoritative provider (opencode-go), a successful discovery is the
// source of truth: baked ids the live endpoint didn't return are pruned, the
// returned ones stay, and brand-new live ids appear.
func TestMergeCatalogAuthoritativePrunesStale(t *testing.T) {
	ids := bakedIDs("opencode-go", 2)
	if len(ids) < 2 {
		t.Skip("need >=2 baked opencode-go models")
	}
	live := []Model{
		{Provider: "opencode-go", ID: ids[0]}, // still offered
		{Provider: "opencode-go", ID: "brand-new-go-model"},
	}
	merged := MergeCatalog(live)

	if !hasModel(merged, "opencode-go", ids[0]) {
		t.Errorf("a discovered baked id should remain: %q", ids[0])
	}
	if !hasModel(merged, "opencode-go", "brand-new-go-model") {
		t.Error("a new live id should appear")
	}
	if hasModel(merged, "opencode-go", ids[1]) {
		t.Errorf("a baked opencode-go id the live endpoint didn't return should be pruned: %q", ids[1])
	}
}

// A non-authoritative provider (opencode, mixed protocol) merges additively:
// discovering a subset must NOT drop its baked entries (e.g. its Anthropic-
// flavour Claude models, which /v1/models can't enumerate).
func TestMergeCatalogKeepsNonAuthoritativeBaked(t *testing.T) {
	ids := bakedIDs("opencode", 1)
	if len(ids) == 0 {
		t.Skip("no baked opencode model")
	}
	live := []Model{{Provider: "opencode", ID: "some-other-discovered-id"}}
	if !hasModel(MergeCatalog(live), "opencode", ids[0]) {
		t.Errorf("baked opencode id %q must survive (additive, not authoritative)", ids[0])
	}
}

// With no discovery for it (offline / no credential), an authoritative
// provider keeps its baked catalog as a fallback.
func TestMergeCatalogOfflineKeepsAuthoritativeBaked(t *testing.T) {
	ids := bakedIDs("opencode-go", 1)
	if len(ids) == 0 {
		t.Skip("no baked opencode-go model")
	}
	if !hasModel(MergeCatalog(nil), "opencode-go", ids[0]) {
		t.Errorf("with no discovery, baked opencode-go %q should remain as a fallback", ids[0])
	}
}

// The user's constraint: a models.json entry survives the authoritative prune,
// even when the live endpoint didn't return it (the manual cross-machine URL
// override case). The user layer is applied after the merge.
func TestUserModelSurvivesAuthoritativePrune(t *testing.T) {
	ResetCatalogLayers()
	defer ResetCatalogLayers()
	ids := bakedIDs("opencode-go", 1)
	if len(ids) == 0 {
		t.Skip("no baked opencode-go model")
	}

	// Discovery returns opencode-go models that do NOT include the baked id, so
	// MergeCatalog prunes it — but a models.json override for it must remain.
	SetLiveModels([]Model{{Provider: "opencode-go", ID: "discovered-only"}})
	SetUserModels([]Model{{Provider: "opencode-go", ID: ids[0], BaseURL: "http://my-box:8000/v1"}})

	got, err := FindModel("opencode-go", ids[0])
	if err != nil {
		t.Fatalf("a models.json entry must survive the prune, got error: %v", err)
	}
	if got.BaseURL != "http://my-box:8000/v1" {
		t.Errorf("user override BaseURL lost: %q", got.BaseURL)
	}
}
