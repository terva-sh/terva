package agent

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// Regression: an openai-compatible model with its own baseUrl in
// models.json must be reached at THAT endpoint, not the base URL stored
// at /login. Previously the login base URL was assigned to args.BaseURL
// before the model was resolved, so the per-model override never applied
// and every model hit the login endpoint regardless of models.json.
func TestResolveModelBaseURLBeatsLoginBaseURL(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())

	// Login-captured openai-compatible endpoint (the default/fallback).
	if err := AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
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

// A model id the endpoint serves but that isn't pinned in models.json
// (open-catalogue) has no baseUrl of its own, so it correctly falls back
// to the login endpoint.
func TestResolveUnregisteredModelFallsBackToLoginBaseURL(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	if err := AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
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
	t.Setenv("TERVA_HOME", t.TempDir())
	if err := AuthStoreFor().SetCompatAPIKey("openai-compatible", "", "https://login.example/v1", "login-default", 32768); err != nil {
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
