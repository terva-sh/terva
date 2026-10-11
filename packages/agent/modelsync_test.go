package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelfiles"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestBedrockCacheRegionAndPartialRefresh(t *testing.T) {
	isolateBedrockRefresh(t)
	t.Setenv("AWS_REGION", "eu-west-1")
	defer modelreg.SetLiveModels(nil)
	cached := provider.ModelCache{
		Version: provider.ModelCacheVersion, FetchedAt: time.Now().UTC(), BedrockRegion: "eu-west-1",
		BedrockFetchedAt: time.Now().UTC(),
		BedrockScope:     build.BedrockDiscoveryScope(),
		Models: []provider.Model{
			{Provider: "amazon-bedrock", ID: "kept", BaseURL: "https://bedrock-runtime.eu-west-1.amazonaws.com"},
			{Provider: "amazon-bedrock", ID: "updated", DisplayName: "old"},
			{Provider: "openai", ID: "other-provider"},
		},
	}
	if !refreshGated(cached, false) {
		t.Fatal("matching region should reuse the cache")
	}
	cfg := provider.BedrockConfig{Region: "eu-west-1", AccessKeyID: "fixture-ak", SecretAccessKey: "fixture-sk"}
	discover := func(ctx context.Context, got provider.BedrockConfig, baseURL string, cat provider.ModelCatalog) ([]provider.Model, error) {
		if got.Region != cfg.Region || got.AccessKeyID != cfg.AccessKeyID || baseURL != "" {
			t.Fatal("discovery did not receive resolved signing config")
		}
		return []provider.Model{{Provider: "amazon-bedrock", ID: "updated", DisplayName: "new"}}, errors.New("partial page failure")
	}
	live, err := refreshBedrockDiscovery(context.Background(), cached, cfg, discover)
	if err == nil || len(live) != 2 || live[0].DisplayName != "new" || live[1].ID != "kept" {
		t.Fatalf("partial refresh lost or replaced results: %+v, %v", live, err)
	}
	t.Setenv("AWS_REGION", "us-west-2")
	if refreshGated(cached, false) {
		t.Fatal("region change must invalidate a fresh cache")
	}
	if err := modelfiles.SaveCache(ModelCachePath(), cached); err != nil {
		t.Fatal(err)
	}
	LoadCachedModels()
	if _, err := modelreg.FindModel("amazon-bedrock", "kept"); err == nil {
		t.Fatal("startup adopted a mismatched-region Bedrock model")
	}
	if _, err := modelreg.FindModel("openai", "other-provider"); err != nil {
		t.Fatal("region change dropped other providers")
	}
	cfg.Region = "us-west-2"
	live, err = refreshBedrockDiscovery(context.Background(), cached, cfg, discover)
	if err == nil || len(live) != 1 {
		t.Fatal("partial refresh retained another region's IDs")
	}
	cached.Version--
	if len(modelsForBedrockRegion(cached, cached.BedrockRegion)) != 1 {
		t.Fatal("legacy cache supplied unscoped Bedrock routes")
	}
}

func TestBedrockRefreshSuccessReplacesCachedIDs(t *testing.T) {
	isolateBedrockRefresh(t)
	cfg := provider.BedrockConfig{Region: "us-east-1", AccessKeyID: "ak", SecretAccessKey: "sk"}
	cached := provider.ModelCache{Version: provider.ModelCacheVersion, BedrockRegion: cfg.Region, BedrockScope: build.BedrockDiscoveryScope(), Models: []provider.Model{{Provider: "amazon-bedrock", ID: "removed"}}}
	success := func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		return nil, nil
	}
	live, err := refreshBedrockDiscovery(context.Background(), cached, cfg, success)
	if err != nil || len(live) != 0 {
		t.Fatal("successful empty discovery retained stale live rows")
	}
	cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Hint = "", "", "signing keys required"
	live, err = refreshBedrockDiscovery(context.Background(), cached, cfg, func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		t.Fatal("missing keys reached the network")
		return nil, nil
	})
	if err == nil || len(live) != 1 {
		t.Fatal("bearer-only discovery should preserve compatible cached rows")
	}
}

func TestBedrockRefreshRetriesAndPersistsRegion(t *testing.T) {
	isolateBedrockRefresh(t)
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-sk")
	defer modelreg.SetLiveModels(nil)
	path := ModelCachePath()
	if err := modelfiles.SaveCache(path, provider.ModelCache{Version: provider.ModelCacheVersion, BedrockRegion: "eu-west-1", BedrockScope: build.BedrockDiscoveryScope(), Models: []provider.Model{{Provider: "amazon-bedrock", ID: "retained"}, {Provider: "openai", ID: "offline-fallback"}}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	refreshModelsWithBedrock(path, false, func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		calls++
		return []provider.Model{{Provider: "amazon-bedrock", ID: "partial"}}, errors.New("fixture partial failure")
	})
	cached, err := modelfiles.LoadCache(path)
	if err != nil || !cached.FetchedAt.IsZero() || !cached.BedrockFetchedAt.IsZero() || !cached.BedrockRetryAt.After(time.Now()) || len(cached.Models) != 3 || cached.BedrockRegion != "eu-west-1" || !cached.BedrockSigning || refreshGated(cached, false) {
		t.Fatalf("failed refresh was lost or treated as fresh: %+v, %v", cached, err)
	}
	if _, err := modelreg.FindModel("amazon-bedrock", "retained"); err != nil {
		t.Fatal("partial refresh did not land retained models")
	}
	if _, err := modelreg.FindModel("openai", "offline-fallback"); err != nil {
		t.Fatal("Bedrock discovery discarded other providers' offline fallback")
	}
	success := func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		calls++
		return []provider.Model{{Provider: "amazon-bedrock", ID: "complete"}}, nil
	}
	// Retry only Bedrock after its bounded cooldown expires.
	cached.BedrockRetryAt = time.Now().Add(-time.Second)
	if err := modelfiles.SaveCache(path, cached); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithBedrock(path, false, success)
	cached, err = modelfiles.LoadCache(path)
	if err != nil || !cached.FetchedAt.IsZero() || cached.BedrockFetchedAt.IsZero() || !cached.BedrockRetryAt.IsZero() || len(cached.Models) != 2 || cached.Models[1].ID != "complete" {
		t.Fatalf("retry did not replace the partial list: %+v, %v", cached, err)
	}
	refreshModelsWithBedrock(path, false, success)
	if calls != 2 {
		t.Fatal("a completed refresh should respect the TTL")
	}
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if refreshGated(cached, false) {
		t.Fatal("a signing capability change must invalidate the cache")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "fixture-ak") || strings.Contains(string(data), "fixture-sk") {
		t.Fatal("model cache contains signing credentials")
	}
}

func isolateBedrockRefresh(t *testing.T) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"DEEPSEEK_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "OPENCODE_API_KEY", "AWS_PROFILE", "AWS_BEARER_TOKEN_BEDROCK", "AWS_SESSION_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(key, "")
	}
}

func TestBedrockProfileScopeAndCallbackRefresh(t *testing.T) {
	isolateBedrockRefresh(t)
	t.Setenv("AWS_PROFILE", "work")
	defer modelreg.SetLiveModels(nil)
	setCLI := func(enabled bool) {
		t.Helper()
		if err := config.SaveConfig(config.Config{Providers: map[string]config.ProviderSettings{"amazon-bedrock": {AWSCLICredentials: enabled}}}); err != nil {
			t.Fatal(err)
		}
	}
	setCLI(true)
	cached := provider.ModelCache{
		Version: provider.ModelCacheVersion, FetchedAt: time.Now().UTC(), BedrockRegion: build.BedrockRegion(),
		BedrockFetchedAt: time.Now().UTC(), BedrockSigning: true, BedrockScope: build.BedrockDiscoveryScope(),
		Models: []provider.Model{{Provider: "amazon-bedrock", ID: "old-profile"}, {Provider: "openai", ID: "unrelated"}},
	}
	if !refreshGated(cached, false) || len(modelsForBedrockRegion(cached, cached.BedrockRegion)) != 2 {
		t.Fatal("positive control: matching profile did not reuse cached models")
	}
	for _, change := range []string{"profile", "adapter"} {
		if change == "profile" {
			t.Setenv("AWS_PROFILE", "other")
		} else {
			t.Setenv("AWS_PROFILE", "work")
			setCLI(false)
		}
		if refreshGated(cached, false) {
			t.Fatalf("%s change reused cache freshness", change)
		}
		rows := modelsForBedrockRegion(cached, cached.BedrockRegion)
		if len(rows) != 1 || rows[0].ID != "unrelated" {
			t.Fatalf("%s change retained another identity's Bedrock rows or lost other providers", change)
		}
	}
	setCLI(true)
	path := ModelCachePath()
	if err := modelfiles.SaveCache(path, cached); err != nil {
		t.Fatal(err)
	}
	calls := 0
	discover := func(_ context.Context, cfg provider.BedrockConfig, _ string, _ provider.ModelCatalog) ([]provider.Model, error) {
		calls++
		if cfg.Credentials == nil || cfg.AccessKeyID != "" || !cfg.HasSigningCredentials() {
			t.Error("callback-only configuration did not reach discovery")
		}
		return nil, errors.New("fixture source unavailable")
	}
	refreshModelsWithBedrock(path, true, discover)
	got, err := modelfiles.LoadCache(path)
	if err != nil || !got.BedrockSigning || got.BedrockScope != "cli:work" || !got.BedrockRetryAt.After(time.Now()) {
		t.Fatalf("callback discovery lost scope or cooldown: %+v, %v", got, err)
	}
	refreshModelsWithBedrock(path, false, discover)
	if calls != 1 {
		t.Fatal("callback failure bypassed the retry cooldown")
	}
	t.Setenv("AWS_PROFILE", "other")
	refreshModelsWithBedrock(path, false, func(_ context.Context, cfg provider.BedrockConfig, _ string, _ provider.ModelCatalog) ([]provider.Model, error) {
		calls++
		return []provider.Model{{Provider: "amazon-bedrock", ID: "new-profile"}}, nil
	})
	got, err = modelfiles.LoadCache(path)
	if err != nil || calls != 2 || got.BedrockScope != "cli:other" || got.BedrockFetchedAt.IsZero() || !got.BedrockRetryAt.IsZero() {
		t.Fatalf("new profile did not refresh independently: %+v, %v", got, err)
	}
	if _, err := modelreg.FindModel("amazon-bedrock", "old-profile"); err == nil {
		t.Fatal("new profile retained the former identity's discovered model")
	}
}

func TestBedrockRefreshDoesNotStarveEndpoints(t *testing.T) {
	isolateBedrockRefresh(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-sk")
	defer modelreg.SetLiveModels(nil)
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		fmt.Fprint(w, `{"data":[{"id":"fixture-local"}]}`)
	}))
	defer server.Close()
	data, err := json.Marshal(config.Config{Endpoints: map[string]config.EndpointConfig{"local-fixture": {BaseURL: server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.TervaHome(), "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithBedrock(ModelCachePath(), false, func(ctx context.Context, _ provider.BedrockConfig, _ string, _ provider.ModelCatalog) ([]provider.Model, error) {
		select {
		case <-requested:
		case <-ctx.Done():
			t.Error("endpoint discovery waited for Bedrock's deadline")
		}
		return []provider.Model{{Provider: "amazon-bedrock", ID: "fixture-bedrock"}}, nil
	})
	if _, err := modelreg.FindModel("local-fixture", "fixture-local"); err != nil {
		t.Fatal("concurrent endpoint results did not reach the registry")
	}
	if _, err := modelreg.FindModel("amazon-bedrock", "fixture-bedrock"); err != nil {
		t.Fatal("concurrent Bedrock results did not reach the registry")
	}
}

func TestBedrockFailureDoesNotInvalidateOtherProviders(t *testing.T) {
	isolateBedrockRefresh(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-sk")
	defer modelreg.SetLiveModels(nil)
	var endpointCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpointCalls.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"replacement"}]}`)
	}))
	defer server.Close()
	data, _ := json.Marshal(config.Config{Endpoints: map[string]config.EndpointConfig{"local-fixture": {BaseURL: server.URL}}})
	if err := os.WriteFile(filepath.Join(config.TervaHome(), "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour).UTC()
	cached := provider.ModelCache{Version: provider.ModelCacheVersion, FetchedAt: stamp, Endpoints: endpointsFingerprint(), BedrockRegion: "us-east-1", BedrockSigning: true, BedrockScope: build.BedrockDiscoveryScope(), Models: []provider.Model{{Provider: "local-fixture", ID: "retained"}}}
	path := ModelCachePath()
	if err := modelfiles.SaveCache(path, cached); err != nil {
		t.Fatal(err)
	}
	var diagnostics []string
	previous := setModelDiscoveryDiagnostic(func(msg string) { diagnostics = append(diagnostics, msg) })
	defer setModelDiscoveryDiagnostic(previous)
	bedrockCalls := 0
	denied := func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		bedrockCalls++
		return nil, errors.New("HTTP 403; check bedrock:ListFoundationModels permission")
	}
	refreshModelsWithBedrock(path, false, denied)
	got, err := modelfiles.LoadCache(path)
	if err != nil || !got.FetchedAt.Equal(stamp) || !got.IsCurrent() || !got.BedrockRetryAt.After(time.Now()) || endpointCalls.Load() != 0 {
		t.Fatalf("Bedrock denial invalidated the other group: %+v, %v", got, err)
	}
	refreshModelsWithBedrock(path, false, denied)
	if bedrockCalls != 1 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "bedrock:ListFoundationModels") {
		t.Fatal("denial bypassed cooldown or missed the host diagnostic sink")
	}
	got.BedrockRetryAt = time.Now().Add(-time.Second)
	if err := modelfiles.SaveCache(path, got); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithBedrock(path, false, denied)
	if bedrockCalls != 2 || endpointCalls.Load() != 0 || len(diagnostics) != 1 {
		t.Fatal("a Bedrock retry rediscovered unrelated providers")
	}
	if _, err := modelreg.FindModel("local-fixture", "retained"); err != nil {
		t.Fatal("Bedrock denial lost the fresh endpoint list")
	}
	refreshModelsWithBedrock(path, true, denied)
	if endpointCalls.Load() != 1 || bedrockCalls != 3 || len(diagnostics) != 2 {
		t.Fatal("a forced refresh did not bypass both provider-group gates")
	}
	if _, err := modelreg.FindModel("local-fixture", "replacement"); err != nil {
		t.Fatal("positive control: the configured endpoint never supplied a replacement")
	}
	t.Setenv("AWS_REGION", "us-west-2")
	refreshModelsWithBedrock(path, false, denied)
	if bedrockCalls != 4 || len(diagnostics) != 3 || endpointCalls.Load() != 1 {
		t.Fatal("a new region suppressed its diagnostic or refreshed other sources")
	}
	refreshModelsWithBedrock(path, true, func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		bedrockCalls++
		return nil, nil
	})
	got, err = modelfiles.LoadCache(path)
	if err != nil || got.BedrockDiagnostic != "" {
		t.Fatal("successful discovery did not clear the previous diagnostic")
	}
	got.BedrockFetchedAt = time.Now().Add(-7 * time.Hour)
	if err := modelfiles.SaveCache(path, got); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithBedrock(path, false, denied)
	if bedrockCalls != 6 || len(diagnostics) != 4 || endpointCalls.Load() != 2 {
		t.Fatal("a failure after success did not report a new notice")
	}
}

// TestRefreshGated_ForceBypassesFreshCache guards the opencode-go symptom: a
// credential added mid-session must re-discover the provider's /v1/models list
// even when a still-fresh cache (written for some other provider) would
// normally short-circuit discovery. A forced refresh ignores the gate; an
// unforced one still honors a current cache.
func TestRefreshGated_ForceBypassesFreshCache(t *testing.T) {
	isolateBedrockRefresh(t)

	// No config.json endpoints, so the live fingerprint is "" — match it.
	current := provider.ModelCache{
		FetchedAt:        time.Now().UTC(),
		Version:          provider.ModelCacheVersion,
		Endpoints:        endpointsFingerprint(),
		BedrockRegion:    build.BedrockRegion(),
		BedrockFetchedAt: time.Now().UTC(),
		BedrockSigning:   build.BedrockDiscoveryConfig().HasSigningCredentials(),
		BedrockScope:     build.BedrockDiscoveryScope(),
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
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)

	ep := config.EndpointConfig{BaseURL: srv.URL + "/v1"}
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"coldbox": ep},
	}); err != nil {
		t.Fatal(err)
	}

	EnsureEndpointModels()

	models := modelreg.ModelsForProvider("coldbox")
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
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)

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
	if got := len(modelreg.ModelsForProvider("deadbox")); got != 0 {
		t.Errorf("invented %d models for a server that answered nothing", got)
	}
}
