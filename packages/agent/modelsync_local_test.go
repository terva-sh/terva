package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
)

func TestNativeEndpointWarmupOverridesAndCacheScope(t *testing.T) {
	isolateBedrockRefresh(t)
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"type":"llm","key":"active","loaded_instances":[{"id":"active","config":{"context_length":8192}}]}]}`)
	}))
	defer srv.Close()
	ep := config.EndpointConfig{BaseURL: srv.URL + "/v1", ModelDiscovery: "lmstudio"}
	if err := config.MutateConfig(func(c *config.Config) { c.Endpoints = map[string]config.EndpointConfig{"local-native": ep} }); err != nil {
		t.Fatal(err)
	}
	EnsureEndpointModels()
	got, err := modelreg.FindModel("local-native", "active")
	if err != nil || got.ContextWindow != 8192 {
		t.Fatalf("warmup did not discover native metadata: %+v, %v", got, err)
	}
	modelreg.SetUserModels([]provider.Model{{Provider: "local-native", ID: "active", ContextWindow: 4096, MaxOutput: 1024}})
	got, err = modelreg.FindModel("local-native", "active")
	if err != nil || got.ContextWindow != 4096 || got.MaxOutput != 1024 {
		t.Fatalf("user overrides lost: %+v, %v", got, err)
	}
	live, err := discoverCompatModels(context.Background(), "local-native", ep.BaseURL, "", 32768, false, provider.AnthropicCompatOptions{}, ep.ModelDiscovery)
	if err != nil || len(live) != 1 || live[0].Provider != "local-native" || live[0].ContextWindow != 8192 {
		t.Fatalf("native warmup did not supply metadata: %+v, %v", live, err)
	}
	cached := provider.ModelCache{Version: provider.ModelCacheVersion, FetchedAt: time.Now(), Endpoints: endpointsFingerprint()}
	if !otherRefreshGated(cached, false) {
		t.Fatal("positive control: matching scope is not cached")
	}
	if err := config.MutateConfig(func(c *config.Config) { ep.ModelDiscovery = "llamacpp"; c.Endpoints["local-native"] = ep }); err != nil {
		t.Fatal(err)
	}
	if otherRefreshGated(cached, false) {
		t.Fatal("native selector change did not invalidate cache")
	}
}

func TestApplyLoginSuccessUsesNativeEndpointDiscovery(t *testing.T) {
	isolateBedrockRefresh(t)
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	t.Cleanup(waitModelRefresh)
	t.Cleanup(func() { build.UnregisterEndpoint("native-login") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("login bypassed native discovery: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"type":"llm","loaded_instances":[{"id":"active","config":{"context_length":8192}}]}]}`)
	}))
	defer srv.Close()
	ep := config.EndpointConfig{BaseURL: srv.URL + "/v1", ModelDiscovery: "lmstudio"}
	if err := config.SaveConfig(config.Config{Endpoints: map[string]config.EndpointConfig{"native-login": ep}}); err != nil {
		t.Fatal(err)
	}
	if err := build.RegisterEndpoint("native-login", ep); err != nil {
		t.Fatal(err)
	}
	var promoted string
	ApplyLoginSuccess(config.AuthStoreFor(), "native-login", func(_, model, _ string) error { promoted = model; return nil })
	got, err := modelreg.FindModel("native-login", "active")
	if err != nil || got.ContextWindow != 8192 || promoted != "active" {
		t.Fatalf("native login adoption: %+v, %v, default=%q", got, err, promoted)
	}
}
