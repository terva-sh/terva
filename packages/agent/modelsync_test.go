package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
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

	must := func(c config.Config) {
		t.Helper()
		b, _ := json.Marshal(c)
		if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(config.Config{Provider: "anthropic", Model: "kimi-for-coding"})

	ValidateAndRepairConfig()

	out, err := config.LoadConfig()
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

	b, _ := json.Marshal(config.Config{Provider: "made-up-provider", Model: "some-model"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
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

	b, _ := json.Marshal(config.Config{Provider: "anthropic", Model: "claude-deleted-model"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
	if out.Provider != "anthropic" {
		t.Errorf("provider changed: %q", out.Provider)
	}
	if out.Model == "" || out.Model == "claude-deleted-model" {
		t.Errorf("model not repaired: %q", out.Model)
	}
}

// A named endpoint is open-catalogue: its models are DISCOVERED, and discovery
// has not run yet on the launch right after /login created it. Repairing the id
// the operator just chose is exactly wrong — an endpoint has no default model,
// so the "repair" is the empty string, and terva then asks the server to run the
// empty model and the turn 400s.
func TestValidateAndRepairConfig_EndpointModelSurvives(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Cleanup(func() { build.UnregisterEndpoint("workshop") })

	b, _ := json.Marshal(config.Config{
		Provider:  "workshop",
		Model:     "qwen2.5-coder",
		Endpoints: map[string]config.EndpointConfig{"workshop": {BaseURL: "http://box:1234/v1"}},
	})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)
	if err := build.RegisterEndpoint("workshop", config.EndpointConfig{BaseURL: "http://box:1234/v1"}); err != nil {
		t.Fatal(err)
	}

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
	if out.Provider != "workshop" {
		t.Errorf("provider = %q, want the endpoint kept", out.Provider)
	}
	if out.Model != "qwen2.5-coder" {
		t.Errorf("model = %q, want qwen2.5-coder kept: an undiscovered id is not a stale one", out.Model)
	}
}

func TestValidateAndRepairConfig_DuplicateModelIDValidForConfiguredProvider(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(config.Config{Provider: "openai-codex", Model: "gpt-5.5"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
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

	b, _ := json.Marshal(config.Config{Provider: "anthropic", Model: "claude-sonnet-4-5"})
	_ = os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
	if out.Provider != "anthropic" {
		t.Errorf("provider mutated: %q", out.Provider)
	}
	if out.Model != "claude-sonnet-4-5" {
		t.Errorf("model mutated: %q", out.Model)
	}
}

// The first launch after an endpoint is created has no cache entry for it, and
// the async refreshes land after the session is already built. Without a
// warm-up, Resolve is choosing between refusing to boot and asking the server to
// run the empty model id — so the startup path fills the catalog first.
func TestEnsureEndpointModelsWarmsAColdCatalog(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"served-coder","max_model_len":16384}]}`)
	}))
	defer srv.Close()

	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	ep := config.EndpointConfig{BaseURL: srv.URL + "/v1"}
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"coldbox": ep},
	}); err != nil {
		t.Fatal(err)
	}

	EnsureEndpointModels()

	models := provider.ModelsForProvider("coldbox")
	if len(models) != 1 {
		t.Fatalf("catalog has %d models for the endpoint, want 1", len(models))
	}
	if models[0].ID != "served-coder" || models[0].BaseURL != ep.BaseURL {
		t.Errorf("model = %+v, want served-coder at the endpoint's URL", models[0])
	}
	if models[0].ContextWindow != 16384 {
		t.Errorf("context window = %d, want the server's 16384", models[0].ContextWindow)
	}

	// A second call must not re-ask: the catalog already answers for this
	// endpoint, and the common launch has to do no I/O at all.
	EnsureEndpointModels()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("the endpoint was queried %d times, want 1: a warm catalog must skip the warm-up", got)
	}
}

// A server that is switched off must never be the reason terva did not start,
// and must not cost the whole discovery timeout on every launch.
func TestEnsureEndpointModelsToleratesADeadServer(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{
			"deadbox": {BaseURL: "http://127.0.0.1:1/v1"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { defer close(done); EnsureEndpointModels() }()
	select {
	case <-done:
	case <-time.After(endpointWarmupTimeout + 2*time.Second):
		t.Fatal("the warm-up outlived its own deadline; startup would hang on an unreachable server")
	}
	if got := len(provider.ModelsForProvider("deadbox")); got != 0 {
		t.Errorf("invented %d models for a server that answered nothing", got)
	}
}
