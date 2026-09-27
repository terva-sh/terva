package build

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/auth"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// noCredentialHome points TERVA_HOME at a fresh directory and blanks every
// provider key the environment could leak into the fallback scan.
func noCredentialHome(t *testing.T) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY", "MOONSHOT_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK", "GROQ_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY",
		"MISTRAL_API_KEY", "TOGETHER_API_KEY", "CEREBRAS_API_KEY", "HF_TOKEN", "ZAI_API_KEY",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
	} {
		t.Setenv(k, "")
	}
	// Bedrock also reads the shared AWS files, so point them at nothing.
	missing := testsupport.TempDir(t)
	t.Setenv("AWS_CONFIG_FILE", missing+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing+"/credentials")
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
}

// A pin on a provider the user has logged out of must fall back onto a shared
// compatible slot when that slot is the only login. The scan used to skip both
// slots, so this boot reported "no credentials found for any provider".
func TestResolveFallsBackToCompatSlot(t *testing.T) {
	noCredentialHome(t)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "anthropic", "claude-opus-4-8"
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetCompatEndpoint(provider.AnthropicCompatProvider, "", auth.CompatEndpoint{
		BaseURL: "http://gateway:4000", Model: "served-model",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != provider.AnthropicCompatProvider {
		t.Fatalf("provider = %q, want the logged-in compatible slot", r.Provider)
	}
	if r.Model != "served-model" {
		t.Errorf("model = %q, want the slot's own model, not the dead pin's", r.Model)
	}
	if r.ProviderSwitch == nil || r.ProviderSwitch.From != "anthropic" {
		t.Errorf("ProviderSwitch = %+v, want a switch recorded from anthropic", r.ProviderSwitch)
	}
}

// A slot with no model cannot run a turn, so falling back onto it would trade a
// boot that /login can fix for a refusal to start.
func TestResolveDoesNotFallBackToModellessCompatSlot(t *testing.T) {
	noCredentialHome(t)
	if err := config.AuthStoreFor().SetCompatEndpoint(provider.AnthropicCompatProvider, "", auth.CompatEndpoint{
		BaseURL: "http://gateway:4000",
	}); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider == provider.AnthropicCompatProvider {
		t.Error("fell back onto a compatible slot that has no model")
	}
}

// A gateway in front of the same vendor serves the same ids, so a fallback onto
// an endpoint that lists the pinned model keeps it.
func TestResolveEndpointFallbackKeepsAListedPin(t *testing.T) {
	noCredentialHome(t)
	ep := config.EndpointConfig{BaseURL: "http://gateway:4000", API: config.EndpointAPIAnthropic}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "anthropic", "claude-opus-4-8"
		c.Endpoints = map[string]config.EndpointConfig{"gw": ep}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint("gw") })
	if err := RegisterEndpoint("gw", ep); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aaa-first", "claude-opus-4-8"} {
		modelreg.RegisterExtraModel(provider.Model{
			Provider: "gw", ID: id, DisplayName: id,
			ContextWindow: 200000, MaxOutput: 8192, BaseURL: ep.BaseURL,
		})
	}

	r, err := Resolve(Args{}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "gw" || r.Model != "claude-opus-4-8" {
		t.Errorf("resolved %s/%s, want gw/claude-opus-4-8", r.Provider, r.Model)
	}
}

// A config pin on a provider this build no longer knows is dropped, not
// replaced with a guess. The old code walked five hardcoded providers, fell to
// anthropic, and wrote anthropic back as the new pin.
func TestResolveDropsAnUnknownConfigProvider(t *testing.T) {
	noCredentialHome(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "gone-provider", "gone-model"
	}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "openai" {
		t.Errorf("provider = %q, want openai (the one reachable provider)", r.Provider)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "" || cfg.Model != "" {
		t.Errorf("config pin = %s/%s, want it cleared rather than rewritten to a guess", cfg.Provider, cfg.Model)
	}
}

// A session file that names a removed provider passes it explicitly. It still
// needs the fallback, because nobody can pick a provider that does not exist.
func TestResolveFallsBackFromAnUnknownExplicitProvider(t *testing.T) {
	noCredentialHome(t)
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "gone-provider", Model: "gone-model"}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "openai" || r.Model == "gone-model" {
		t.Errorf("resolved %s/%s, want openai on its own model", r.Provider, r.Model)
	}
}

// An unknown provider named explicitly, beside a known config pin, lands on
// the pin as if nothing had been named. It must not report a lapse on an
// anthropic pin nobody made.
func TestResolveUnknownExplicitProviderLandsOnTheKnownPin(t *testing.T) {
	noCredentialHome(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "openai", "gpt-5"
	}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Provider: "gone-provider", Model: "gone-model"}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "openai" || r.Model != "gpt-5" {
		t.Errorf("resolved %s/%s, want the configured openai/gpt-5", r.Provider, r.Model)
	}
	if r.ProviderSwitch != nil {
		t.Errorf("ProviderSwitch = %+v, want none: the pin worked", r.ProviderSwitch)
	}
}

// A named endpoint outranks a shared compatible slot in the fallback. Before
// the slots joined the scan, a user with both booted onto the endpoint, and a
// stale slot login must not take that away.
func TestResolveFallbackPrefersAnEndpointOverACompatSlot(t *testing.T) {
	noCredentialHome(t)
	ep := config.EndpointConfig{BaseURL: "http://ep:9000/v1"}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{"ep-first": ep}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterEndpoint("ep-first") })
	if err := RegisterEndpoint("ep-first", ep); err != nil {
		t.Fatal(err)
	}
	modelreg.RegisterExtraModel(provider.Model{
		Provider: "ep-first", ID: "local-llm", DisplayName: "local-llm",
		ContextWindow: 8192, MaxOutput: 4096, BaseURL: ep.BaseURL,
	})
	if err := config.AuthStoreFor().SetCompatEndpoint(provider.AnthropicCompatProvider, "", auth.CompatEndpoint{
		BaseURL: "http://gateway:4000", Model: "served-model",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "ep-first" {
		t.Errorf("provider = %q, want the named endpoint ahead of the compatible slot", r.Provider)
	}
}

// An --api-key given with an unknown --provider must not travel to the
// provider the fallback lands on. Nothing says which vendor issued it.
func TestResolveUnknownProviderDropsItsAPIKey(t *testing.T) {
	noCredentialHome(t)
	t.Setenv("OPENAI_API_KEY", "env-openai-key")

	r, err := Resolve(Args{Provider: "gone-provider", APIKey: "key-for-gone"}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", r.Provider)
	}
	if r.Credential == "key-for-gone" {
		t.Error("the unknown provider's --api-key was sent to openai")
	}
}

// An unknown provider in the project layer must not hide a user pin that
// still works.
func TestResolveUnknownProjectPinFallsToTheUserPin(t *testing.T) {
	noCredentialHome(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "deepseek", "deepseek-chat"
	}); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)
	if err := config.SetProjectModel(cwd, "gone-provider", "gone-model"); err != nil {
		t.Fatal(err)
	}
	if err := config.TrustPath(cwd, false); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{CWD: cwd}, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "deepseek" {
		t.Errorf("provider = %q, want the user pin deepseek", r.Provider)
	}
}

// ProviderConfigured is a presence check. An expired login counts, and an
// absent one does not.
func TestProviderConfiguredCountsAnExpiredLogin(t *testing.T) {
	noCredentialHome(t)
	if ProviderConfigured("anthropic") {
		t.Fatal("anthropic is configured with nothing stored")
	}
	if err := config.AuthStoreFor().SetOAuth("anthropic", auth.OAuthToken{
		AccessToken: "expired-access-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if !ProviderConfigured("anthropic") {
		t.Error("an expired anthropic login does not count as configured")
	}
	if !ProviderConfigured("ollama") {
		t.Error("ollama needs no credential and must count as configured")
	}
}
