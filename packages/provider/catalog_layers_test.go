package provider

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestExtraModelsSurviveLiveRefresh is the regression for the overlay
// race (deep-review Tier 1 #10): the standard live refresh and the
// compat discovery run concurrently at startup, and under the old
// single-overlay design whichever SetLiveModels landed second wiped
// every RegisterExtraModel entry.
func TestExtraModelsSurviveLiveRefresh(t *testing.T) {
	withCatalogState(t)
	testReg.Reset()

	testReg.RegisterExtraModel(Model{
		Provider: "openai-compatible", ID: "local-qwen",
		DisplayName: "local-qwen", BaseURL: "http://localhost:1234/v1",
		Source: "openai-compatible",
	})
	testReg.SetLiveModels([]Model{{
		Provider: "anthropic", ID: "live-model", DisplayName: "Live", Source: "live",
	}})

	if _, err := testReg.FindModel("openai-compatible", "local-qwen"); err != nil {
		t.Fatalf("extra model wiped by live refresh: %v", err)
	}
	if _, err := testReg.FindModel("anthropic", "live-model"); err != nil {
		t.Fatalf("live model missing: %v", err)
	}
}

// TestUserOverridesSurviveRefreshes: models.json entries must persist
// no matter how often or in what order the other layers refresh.
func TestUserOverridesSurviveRefreshes(t *testing.T) {
	withCatalogState(t)
	testReg.Reset()

	testReg.SetUserModels([]Model{{
		Provider: "openai-compatible", ID: "gemma-local",
		MaxOutput: 32768, BaseURL: "http://localhost:1234/v1", Source: "user",
	}})

	// Refreshes land afterwards, in both orders.
	testReg.RegisterExtraModel(Model{
		Provider: "openai-compatible", ID: "gemma-local",
		DisplayName: "gemma-local", MaxOutput: 8192,
		BaseURL: "http://localhost:1234/v1", Source: "openai-compatible",
	})
	testReg.SetLiveModels([]Model{{Provider: "anthropic", ID: "whatever", Source: "live"}})

	got, err := testReg.FindModel("openai-compatible", "gemma-local")
	if err != nil {
		t.Fatalf("FindModel: %v", err)
	}
	if got.MaxOutput != 32768 {
		t.Fatalf("user MaxOutput lost after refreshes: got %d, want 32768", got.MaxOutput)
	}
	if got.Source != "user" {
		t.Fatalf("Source = %q, want user", got.Source)
	}
}

// TestLayerPrecedence pins user > extra > live for the same model key.
func TestLayerPrecedence(t *testing.T) {
	withCatalogState(t)
	testReg.Reset()

	key := func() Model {
		m, err := testReg.FindModel("openai-compatible", "shared-id")
		if err != nil {
			t.Fatalf("FindModel: %v", err)
		}
		return m
	}

	testReg.SetLiveModels([]Model{{
		Provider: "openai-compatible", ID: "shared-id",
		DisplayName: "from-live", MaxOutput: 1000, Source: "live",
	}})
	if m := key(); m.MaxOutput != 1000 {
		t.Fatalf("live layer not visible: %+v", m)
	}

	testReg.RegisterExtraModel(Model{
		Provider: "openai-compatible", ID: "shared-id",
		DisplayName: "from-extra", MaxOutput: 2000, Source: "openai-compatible",
	})
	if m := key(); m.MaxOutput != 2000 || m.DisplayName != "from-extra" {
		t.Fatalf("extra should beat live: %+v", m)
	}

	testReg.SetUserModels([]Model{{
		Provider: "openai-compatible", ID: "shared-id",
		DisplayName: "from-user", MaxOutput: 3000, Source: "user",
	}})
	if m := key(); m.MaxOutput != 3000 || m.DisplayName != "from-user" {
		t.Fatalf("user should beat extra: %+v", m)
	}

	// A later live refresh must not demote the user view.
	testReg.SetLiveModels([]Model{{
		Provider: "openai-compatible", ID: "shared-id",
		DisplayName: "from-live-2", MaxOutput: 1500, Source: "live",
	}})
	if m := key(); m.MaxOutput != 3000 {
		t.Fatalf("live refresh demoted user override: %+v", m)
	}
}

// TestPriceOnlyOverrideKeepsReasoning is the regression for the
// Reasoning *bool fix, exercised through real models.json parsing: an
// entry that only sets prices must not flip a reasoning model to
// non-reasoning (which produced 400s from OpenAI reasoning models).
func TestPriceOnlyOverrideKeepsReasoning(t *testing.T) {
	withCatalogState(t)
	testReg.Reset()

	testReg.SetLiveModels([]Model{{
		Provider: "openai", ID: "o9-mini", DisplayName: "o9 mini",
		Reasoning: true, MaxOutput: 100000, Source: "live",
	}})

	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{
		"providers": {
			"openai": {
				"models": [
					{"id": "o9-mini", "priceInput": 1.25, "priceOutput": 10.0}
				]
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := loadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	testReg.SetUserOverrides(overrides)

	got, err := testReg.FindModel("openai", "o9-mini")
	if err != nil {
		t.Fatalf("FindModel: %v", err)
	}
	if !got.Reasoning {
		t.Fatalf("price-only override disabled reasoning")
	}
	if got.PriceInput != 1.25 || got.PriceOutput != 10.0 {
		t.Fatalf("prices not applied: %+v", got)
	}

	// An explicit "reasoning": false still applies.
	if err := os.WriteFile(path, []byte(`{
		"providers": {
			"openai": {
				"models": [
					{"id": "o9-mini", "reasoning": false}
				]
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, _ = loadUserModelsWithWarnings(path)
	testReg.SetUserOverrides(overrides)
	if got, _ := testReg.FindModel("openai", "o9-mini"); got.Reasoning {
		t.Fatalf("explicit reasoning:false ignored")
	}
}

// TestCatalogLayerWritesAreConcurrencySafe drives all three setters and
// readers from concurrent goroutines; run under -race this pins the
// structural fix for the startup refresh race.
func TestCatalogLayerWritesAreConcurrencySafe(t *testing.T) {
	withCatalogState(t)
	testReg.Reset()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			testReg.SetLiveModels([]Model{{Provider: "anthropic", ID: "live", Source: "live"}})
		}()
		go func() {
			defer wg.Done()
			testReg.RegisterExtraModel(Model{Provider: "openai-compatible", ID: "extra", Source: "openai-compatible"})
		}()
		go func() {
			defer wg.Done()
			testReg.SetUserModels([]Model{{Provider: "openai", ID: "user", Source: "user"}})
		}()
		go func() {
			defer wg.Done()
			_ = testReg.Active()
			_, _ = testReg.FindModel("openai-compatible", "extra")
		}()
	}
	wg.Wait()

	// After the dust settles every layer's contribution is present.
	for _, k := range [][2]string{{"anthropic", "live"}, {"openai-compatible", "extra"}, {"openai", "user"}} {
		if _, err := testReg.FindModel(k[0], k[1]); err != nil {
			t.Errorf("%s/%s missing after concurrent writes: %v", k[0], k[1], err)
		}
	}
}
