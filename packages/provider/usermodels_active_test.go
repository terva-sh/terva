package provider

import "testing"

// Regression: SetUserModels must make its models visible through
// Active() even when no live overlay has been set yet (the fresh-install
// `terva --list-models` path, before any model cache exists). Before the
// fix, models were appended to a nil `active` while activeSet stayed
// false, so Active() fell back to the static Catalog and dropped them.
func TestSetUserModelsVisibleWithoutLiveOverlay(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers() // simulate a never-discovered catalog

	SetUserModels([]Model{{
		Provider:      "openai-compatible",
		ID:            "local-qwen",
		DisplayName:   "Local Qwen",
		ContextWindow: 32768,
		MaxOutput:     8192,
		BaseURL:       "http://localhost:1234/v1",
		Source:        "user",
	}})

	got, err := FindModel("openai-compatible", "local-qwen")
	if err != nil {
		t.Fatalf("user model not visible via FindModel without a live overlay: %v", err)
	}
	if got.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("BaseURL = %q, want the models.json value", got.BaseURL)
	}

	// The static catalog must remain alongside the user overlay, not be
	// clobbered by it.
	if len(ModelsForProvider("anthropic")) == 0 {
		t.Fatal("catalog models dropped after SetUserModels seeded the overlay")
	}

	// And the model must actually appear in the full Active() listing
	// that --list-models prints.
	var found bool
	for _, m := range Active() {
		if m.Provider == "openai-compatible" && m.ID == "local-qwen" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("user model absent from Active() listing")
	}
}

// Regression: a models.json override must win over the openai-compatible
// default-model registration, even when that registration ran first.
// LoadCompatModel/RegisterExtraModel writes the login default model with
// a generic 8192 max-output; SetUserModels (applied last) must override
// the user-set fields rather than the compat entry masking them.
func TestSetUserModelsOverridesCompatRegistration(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers() // simulate a never-discovered catalog

	// Simulate LoadCompatModel: login default model, generic 8192 cap.
	RegisterExtraModel(Model{
		Provider:      "openai-compatible",
		ID:            "gemma-local",
		DisplayName:   "gemma-local",
		ContextWindow: 196608,
		MaxOutput:     8192,
		BaseURL:       "http://localhost:1234/v1",
		Source:        "openai-compatible",
	})

	// The user's models.json pins a larger response cap for the same id.
	SetUserModels([]Model{{
		Provider:      "openai-compatible",
		ID:            "gemma-local",
		ContextWindow: 196608,
		MaxOutput:     32768,
		BaseURL:       "http://localhost:1234/v1",
		Source:        "user",
	}})

	got, err := FindModel("openai-compatible", "gemma-local")
	if err != nil {
		t.Fatalf("FindModel: %v", err)
	}
	if got.MaxOutput != 32768 {
		t.Fatalf("MaxOutput = %d, want 32768 (models.json must win over the compat 8192 default)", got.MaxOutput)
	}
	if got.ContextWindow != 196608 {
		t.Fatalf("ContextWindow = %d, want 196608", got.ContextWindow)
	}
	if got.Source != "user" {
		t.Fatalf("Source = %q, want user", got.Source)
	}
	// The override must not have duplicated the entry.
	if n := len(ModelsForProvider("openai-compatible")); n != 1 {
		t.Fatalf("expected 1 openai-compatible model, got %d", n)
	}
}
