package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Regression: an openai-compatible model with its own baseUrl in
// models.json must be reached at THAT endpoint, not the base URL stored
// at /login. Previously the login base URL was assigned to args.BaseURL
// before the model was resolved, so the per-model override never applied
// and every model hit the login endpoint regardless of models.json.
func TestResolveModelBaseURLBeatsLoginBaseURL(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	// Login-captured openai-compatible endpoint (the default/fallback).
	if err := config.AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
		t.Fatal(err)
	}

	// A models.json model pinned to a DIFFERENT endpoint.
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider:      "openai-compatible",
		ID:            "model-on-gateway",
		DisplayName:   "Model On Gateway",
		ContextWindow: 131072,
		MaxOutput:     16384,
		BaseURL:       "https://gateway.example/v1",
		Source:        "user",
	}})

	r, err := Resolve(Args{Provider: "openai-compatible", Model: "model-on-gateway"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.BaseURL != "https://gateway.example/v1" {
		t.Fatalf("BaseURL = %q, want the per-model models.json baseUrl https://gateway.example/v1 (not the login default)", r.BaseURL)
	}
}

// Regression: the open-catalogue fallback must register its synthesized
// model in the active catalog. Auto-compaction, the status-bar gauge,
// and /status all read the context window via provider.FindModel — an
// unregistered model left them all seeing 0, so auto-compaction never
// fired and the configured compat context window was silently dropped.
func TestResolveRegistersOpenCatalogueModel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 192000); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	if _, err := Resolve(Args{Provider: "openai-compatible", Model: "some-unregistered-model"}, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m, err := provider.FindModel("openai-compatible", "some-unregistered-model")
	if err != nil {
		t.Fatalf("synthesized model not in the active catalog: %v", err)
	}
	if m.ContextWindow != 192000 {
		t.Fatalf("ContextWindow = %d, want the configured compat default 192000", m.ContextWindow)
	}
}

// A model id the endpoint serves but that isn't pinned in models.json
// (open-catalogue) has no baseUrl of its own, so it correctly falls back
// to the login endpoint.
func TestResolveUnregisteredModelFallsBackToLoginBaseURL(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	r, err := Resolve(Args{Provider: "openai-compatible", Model: "some-unregistered-model"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.BaseURL != "https://login.example/v1" {
		t.Fatalf("BaseURL = %q, want the login fallback https://login.example/v1", r.BaseURL)
	}
}

// An explicit --base-url flag still wins over both.
func TestResolveBaseURLFlagBeatsEverything(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider: "openai-compatible", ID: "model-on-gateway",
		ContextWindow: 131072, MaxOutput: 16384,
		BaseURL: "https://gateway.example/v1", Source: "user",
	}})

	r, err := Resolve(Args{Provider: "openai-compatible", Model: "model-on-gateway", BaseURL: "https://flag.example/v1"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.BaseURL != "https://flag.example/v1" {
		t.Fatalf("BaseURL = %q, want the --base-url flag https://flag.example/v1", r.BaseURL)
	}
}
