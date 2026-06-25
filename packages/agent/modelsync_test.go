package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestRefreshGated_ForceBypassesFreshCache guards the opencode-go symptom: a
// credential added mid-session must re-discover the provider's /v1/models list
// even when a still-fresh cache (written for some other provider) would
// normally short-circuit discovery. A forced refresh ignores the gate; an
// unforced one still honors a current cache.
func TestRefreshGated_ForceBypassesFreshCache(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	// No config.json endpoints, so the live fingerprint is "" — match it.
	current := provider.ModelCache{
		FetchedAt: time.Now().UTC(),
		Version:   provider.ModelCacheVersion,
		Endpoints: endpointsFingerprint(),
	}

	if !refreshGated(current, false) {
		t.Fatal("a current cache should gate an unforced refresh")
	}
	if refreshGated(current, true) {
		t.Fatal("force must bypass the cache gate so a fresh login re-discovers")
	}

	// A stale (zero FetchedAt) or absent cache never gates, forced or not.
	if refreshGated(provider.ModelCache{}, false) {
		t.Fatal("a stale/absent cache should never gate a refresh")
	}
}

// TestValidateAndRepairConfig_MismatchedPair simulates the bug from a
// stale /model switch: provider=anthropic but model=kimi-for-coding
// (which belongs to provider=kimi). The validator should rewrite the
// model to anthropic's default and persist.
func TestValidateAndRepairConfig_MismatchedPair(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	must := func(c Config) {
		t.Helper()
		b, _ := json.Marshal(c)
		if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(Config{Provider: "anthropic", Model: "kimi-for-coding"})

	ValidateAndRepairConfig()

	out, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "anthropic" {
		t.Errorf("provider not preserved: %q", out.Provider)
	}
	if out.Model == "kimi-for-coding" {
		t.Errorf("model not repaired; still %q", out.Model)
	}
	if out.Model == "" {
		t.Errorf("model not set; expected anthropic default")
	}
}

// TestValidateAndRepairConfig_UnknownProvider resets to anthropic and
// clears the model when the saved provider id isn't recognised
// (e.g. user removed it from a previous build).
func TestValidateAndRepairConfig_UnknownProvider(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(Config{Provider: "made-up-provider", Model: "some-model"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := LoadConfig()
	if out.Provider != "anthropic" {
		t.Errorf("provider not reset: %q", out.Provider)
	}
	if out.Model != "" {
		t.Errorf("model not cleared: %q", out.Model)
	}
}

// TestValidateAndRepairConfig_UnknownModel keeps the provider but
// snaps the model to that provider's default when the saved id is no
// longer in the catalog.
func TestValidateAndRepairConfig_UnknownModel(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(Config{Provider: "anthropic", Model: "claude-deleted-model"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := LoadConfig()
	if out.Provider != "anthropic" {
		t.Errorf("provider changed: %q", out.Provider)
	}
	if out.Model == "" || out.Model == "claude-deleted-model" {
		t.Errorf("model not repaired: %q", out.Model)
	}
}

func TestValidateAndRepairConfig_DuplicateModelIDValidForConfiguredProvider(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(Config{Provider: "openai-codex", Model: "gpt-5.5"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := LoadConfig()
	if out.Provider != "openai-codex" {
		t.Errorf("provider mutated: %q", out.Provider)
	}
	if out.Model != "gpt-5.5" {
		t.Errorf("model mutated: %q", out.Model)
	}
}

// TestValidateAndRepairConfig_HappyPath leaves a valid config alone.
func TestValidateAndRepairConfig_HappyPath(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(Config{Provider: "anthropic", Model: "claude-sonnet-4-5"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := LoadConfig()
	if out.Provider != "anthropic" {
		t.Errorf("provider mutated: %q", out.Provider)
	}
	if out.Model != "claude-sonnet-4-5" {
		t.Errorf("model mutated: %q", out.Model)
	}
}
