package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A bare lookup can find another provider's copy of a shared ID first.
// Resolve must check the requested provider before substituting its default.
func TestResolveKeepsAModelListedUnderSeveralProviders(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	const id = "test-shared-model"
	modelreg.SetUserModels([]provider.Model{
		{Provider: "azure-openai-responses", ID: id},
		{Provider: "openai", ID: id},
	})
	bare, err := modelreg.FindModel("", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelreg.FindModel("openai", id); err != nil {
		t.Fatal(err)
	}
	if bare.Provider != "azure-openai-responses" {
		t.Fatalf("fixture must resolve the other provider first, got %q", bare.Provider)
	}

	r, err := Resolve(Args{Provider: "openai", Model: id}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "openai" {
		t.Errorf("provider = %q, want openai", r.Provider)
	}
	if r.Model != id {
		t.Errorf("model = %q, want %q — an explicitly named model was replaced by the "+
			"provider's default because a bare catalogue lookup found %q's copy first",
			r.Model, id, bare.Provider)
	}
}

// The behaviour the narrowed check must not lose: an id this provider genuinely
// does not have, which another one does, is still replaced by the default.
func TestResolveStillDropsAModelThisProviderDoesNotHave(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "anthropic", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", r.Provider)
	}
	if r.Model == "gpt-5" {
		t.Error("anthropic resolved with an openai model id — the incoherent pair was kept")
	}
}
