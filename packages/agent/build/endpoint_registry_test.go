package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Saving an endpoint that already exists is an EDIT, not a collision: /login
// re-saves the same name every time an operator corrects a port. It must not
// fail, and it must not leave two rows in the id list for one backend.
func TestRegisterOrReplaceEndpointIsAnEdit(t *testing.T) {
	t.Cleanup(func() { UnregisterEndpoint("ep-repoint") })

	if err := RegisterOrReplaceEndpoint("ep-repoint", config.EndpointConfig{BaseURL: "http://old:1111/v1"}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	// Plain RegisterEndpoint is the thing that cannot do this.
	if err := RegisterEndpoint("ep-repoint", config.EndpointConfig{BaseURL: "http://new:2222/v1"}); err == nil {
		t.Fatal("RegisterEndpoint should still refuse a name it already holds")
	}
	if err := RegisterOrReplaceEndpoint("ep-repoint", config.EndpointConfig{BaseURL: "http://new:2222/v1"}); err != nil {
		t.Fatalf("re-saving an existing endpoint: %v", err)
	}

	var n int
	for _, id := range ProviderIDs() {
		if id == "ep-repoint" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("endpoint appears %d times in the provider list, want exactly 1", n)
	}
	if !IsKnownProvider("ep-repoint") {
		t.Error("the replaced endpoint is no longer registered")
	}
}

// A built-in provider is never removable, whatever id is passed. The registry
// holds built-ins and endpoints in the same map, so without the endpoint-origin
// check an UnregisterEndpoint("anthropic") would leave the process unable to
// resolve anthropic at all.
func TestUnregisterEndpointRefusesBuiltIn(t *testing.T) {
	UnregisterEndpoint("anthropic")
	if !IsKnownProvider("anthropic") {
		t.Fatal("UnregisterEndpoint removed a built-in provider")
	}
	if _, ok := specFor("anthropic"); !ok {
		t.Fatal("anthropic lost its registry spec")
	}
}

// RegisterOrReplaceEndpoint still refuses a built-in's name: "replace" applies
// to endpoints this package registered, never to a provider terva ships.
func TestRegisterOrReplaceEndpointRefusesBuiltIn(t *testing.T) {
	if err := RegisterOrReplaceEndpoint("anthropic", config.EndpointConfig{BaseURL: "http://x/v1"}); err == nil {
		t.Fatal("an endpoint may not displace a built-in provider")
	}
	if !IsKnownProvider("anthropic") {
		t.Fatal("the refused registration still damaged the registry")
	}
}

// An endpoint's default model comes from the catalog, because an endpoint has no
// baked-in one. Empty when discovery has found it nothing — the caller must
// treat that as "no model", not as a usable id.
func TestEndpointDefaultModel(t *testing.T) {
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	if got := EndpointDefaultModel("ep-nomodels"); got != "" {
		t.Errorf("EndpointDefaultModel with nothing discovered = %q, want empty", got)
	}
	provider.RegisterExtraModel(provider.Model{
		Provider: "ep-models", ID: "qwen-local", DisplayName: "qwen-local",
		ContextWindow: 8192, MaxOutput: 4096,
	})
	if got := EndpointDefaultModel("ep-models"); got != "qwen-local" {
		t.Errorf("EndpointDefaultModel = %q, want qwen-local", got)
	}
}

// Resolve must never send an empty model id to an endpoint. DefaultModelForProvider
// returns "" for an endpoint (it has no baked-in default), and "" is not a
// placeholder — it is serialized into the request body and the server rejects the
// turn. With models discovered, take one; with none, say so.
func TestResolveEndpointWithoutModel(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	ep := config.EndpointConfig{BaseURL: "http://ep-nomodel:9000/v1"}
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"ep-nomodel": ep},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint("ep-nomodel") })
	if err := RegisterEndpoint("ep-nomodel", ep); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(Args{Provider: "ep-nomodel"}, false)
	if err == nil {
		t.Fatal("Resolve accepted an endpoint with no model at all")
	}

	// Once discovery has found the endpoint a model, that model is the default.
	provider.RegisterExtraModel(provider.Model{
		Provider: "ep-nomodel", ID: "served-model", DisplayName: "served-model",
		ContextWindow: 8192, MaxOutput: 4096, BaseURL: ep.BaseURL,
	})
	r, err := Resolve(Args{Provider: "ep-nomodel"}, false)
	if err != nil {
		t.Fatalf("Resolve with a discovered model: %v", err)
	}
	if r.Model != "served-model" {
		t.Errorf("model = %q, want served-model (never the empty id)", r.Model)
	}
}

// The credential fallback scan walks the registry, so a registered endpoint is a
// candidate like any other. Without this a user whose ONLY backend is their own
// server gets "no credential for anthropic" — terva falling back past the one
// thing they can actually reach.
func TestResolveFallsBackToEndpointWhenNothingElseIsLoggedIn(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK", "GROQ_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY",
		"MISTRAL_API_KEY", "TOGETHER_API_KEY", "CEREBRAS_API_KEY", "HF_TOKEN", "ZAI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}

	ep := config.EndpointConfig{BaseURL: "http://ep-only:9000/v1"}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{"ep-only": ep}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint("ep-only") })
	if err := RegisterEndpoint("ep-only", ep); err != nil {
		t.Fatal(err)
	}
	provider.RegisterExtraModel(provider.Model{
		Provider: "ep-only", ID: "local-llm", DisplayName: "local-llm",
		ContextWindow: 8192, MaxOutput: 4096, BaseURL: ep.BaseURL,
	})

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "ep-only" {
		t.Fatalf("provider = %q, want ep-only (fell back past the only reachable backend)", r.Provider)
	}
	if r.CredentialErr != nil {
		t.Errorf("credential error on a keyless endpoint: %v", r.CredentialErr)
	}
}

// ...but only onto an endpoint that can actually run something. An endpoint
// whose models have not been discovered — a server that was off at startup — has
// no model id to offer, and falling back onto it would turn "not logged in",
// which boots and which /login can fix, into a refusal to start at all.
func TestResolveDoesNotFallBackToAModellessEndpoint(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK", "GROQ_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY",
		"MISTRAL_API_KEY", "TOGETHER_API_KEY", "CEREBRAS_API_KEY", "HF_TOKEN", "ZAI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}

	ep := config.EndpointConfig{BaseURL: "http://ep-cold:9000/v1"}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{"ep-cold": ep}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint("ep-cold") })
	if err := RegisterEndpoint("ep-cold", ep); err != nil {
		t.Fatal(err)
	}
	// Deliberately no models registered for it.

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve refused to start over an endpoint with no models: %v", err)
	}
	if r.Provider == "ep-cold" {
		t.Error("fell back onto an endpoint that has no model to run")
	}
	if r.CredentialErr == nil {
		t.Error("reported a usable credential when there is none")
	}
}
