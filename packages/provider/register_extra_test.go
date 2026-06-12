package provider

import "testing"

func TestRegisterExtraModel(t *testing.T) {
	// Save and restore the catalog layers so this test doesn't leak
	// state into others that read Active().
	withCatalogState(t)
	ResetCatalogLayers() // simulate a never-discovered catalog

	m := Model{
		Provider:    "openai-compatible",
		ID:          "local-qwen",
		DisplayName: "local-qwen",
		BaseURL:     "http://localhost:1234/v1",
		Source:      "openai-compatible",
	}
	RegisterExtraModel(m)

	got, err := FindModel("openai-compatible", "local-qwen")
	if err != nil {
		t.Fatalf("FindModel: %v", err)
	}
	if got.BaseURL != m.BaseURL {
		t.Fatalf("BaseURL=%q", got.BaseURL)
	}

	// The baked-in catalog must still be present alongside the overlay.
	if len(ModelsForProvider("anthropic")) == 0 {
		t.Fatal("catalog models dropped after RegisterExtraModel")
	}

	// Re-registering the same id replaces rather than duplicates.
	RegisterExtraModel(Model{Provider: "openai-compatible", ID: "local-qwen", BaseURL: "http://localhost:9999/v1"})
	if n := len(ModelsForProvider("openai-compatible")); n != 1 {
		t.Fatalf("expected 1 compat model after re-register, got %d", n)
	}
	got, _ = FindModel("openai-compatible", "local-qwen")
	if got.BaseURL != "http://localhost:9999/v1" {
		t.Fatalf("re-register did not replace BaseURL: %q", got.BaseURL)
	}
}
