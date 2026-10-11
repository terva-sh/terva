package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelfiles"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
)

func TestDeepSeekRefreshCredentialsCacheAndOverrides(t *testing.T) {
	isolateBedrockRefresh(t)
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	t.Cleanup(func() { modelreg.SetUserModels(nil) })
	t.Setenv("DEEPSEEK_API_KEY", "fixture-deepseek-key")
	var fail atomic.Bool
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("Authorization") == "Bearer fixture-deepseek-key" {
			calls.Add(1)
			if fail.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"deepseek-fixture-new-flash"},{"id":"deepseek-v4-flash"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"other-endpoint-model"}]}`)
	}))
	defer srv.Close()
	discover := func(ctx context.Context, key, baseURL string) ([]provider.Model, error) {
		if key != "fixture-deepseek-key" || baseURL != "" {
			t.Error("harness did not use the resolved direct credential")
			return nil, fmt.Errorf("unexpected discovery credential or base URL")
		}
		return provider.DiscoverDeepSeek(ctx, key, srv.URL)
	}
	bedrock := func(context.Context, provider.BedrockConfig, string, provider.ModelCatalog) ([]provider.Model, error) {
		t.Error("unconfigured Bedrock reached network")
		return nil, fmt.Errorf("unconfigured Bedrock discovery")
	}
	cached := provider.ModelCache{Version: provider.ModelCacheVersion, FetchedAt: time.Now(),
		BedrockFetchedAt: time.Now(), BedrockRegion: build.BedrockRegion(), BedrockScope: build.BedrockDiscoveryScope()}
	path := ModelCachePath()
	if err := modelfiles.SaveCache(path, cached); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithDiscovery(path, false, bedrock, discover)
	if calls.Load() != 0 {
		t.Fatal("fresh cache did not gate discovery")
	}
	modelreg.SetUserModels([]provider.Model{{Provider: "deepseek", ID: "deepseek-fixture-new-flash", ContextWindow: 65536, MaxOutput: 2048, Reasoning: true}})
	refreshModelsWithDiscovery(path, true, bedrock, discover)
	got, err := modelreg.FindModel("deepseek", "deepseek-fixture-new-flash")
	if err != nil || calls.Load() != 1 || got.ContextWindow != 65536 || got.MaxOutput != 2048 || !got.Reasoning {
		t.Fatalf("forced refresh lost overrides: %+v, %v, calls=%d", got, err, calls.Load())
	}
	cached, err = modelfiles.LoadCache(path)
	if err != nil || len(cached.Models) != 2 || cached.FetchedAt.IsZero() {
		t.Fatalf("live IDs not cached: %+v, %v", cached, err)
	}
	modelreg.SetUserModels(nil)
	LoadCachedModels()
	if _, err := modelreg.FindModel("deepseek", "deepseek-fixture-new-flash"); err != nil {
		t.Fatal("cached discovery did not restore new ID")
	}
	fail.Store(true)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{"fixture-other": {BaseURL: srv.URL}}
	}); err != nil {
		t.Fatal(err)
	}
	refreshModelsWithDiscovery(path, true, bedrock, discover)
	cached, err = modelfiles.LoadCache(path)
	if err != nil || len(cached.Models) != 3 {
		t.Fatalf("another provider erased offline DeepSeek rows: %+v, %v", cached, err)
	}
	if _, err := modelreg.FindModel("fixture-other", "other-endpoint-model"); err != nil {
		t.Fatal("positive control: other provider never refreshed")
	}
}
