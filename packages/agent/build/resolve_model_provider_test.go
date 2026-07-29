package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Resolve drops a model that belongs to a DIFFERENT provider than the one it
// resolved — config naming gpt-5 while the provider fell back to anthropic is
// an incoherent pair, and the provider's default is the better answer.
//
// It used to ask that question of a BARE catalogue lookup, which returns the
// first match across every provider. Several ids are listed under more than one:
// gpt-5-pro and gpt-5-codex appear under azure-openai-responses before openai.
// So for openai/gpt-5-pro the bare answer was "that is an azure model", the pair
// looked incoherent, and Resolve silently substituted openai's default. The user
// asked for gpt-5-pro by name — on the command line, in config, or through
// /model — and got gpt-5, with no warning anywhere (the warn-and-repair path
// below handles unknown ids, and this id is perfectly well known).
//
// This is the same provider-hop the /model verb guards against by resolving a
// bare id against the current provider first. Resolve is handed the provider
// outright, so it never had to guess.
func TestResolveKeepsAModelListedUnderSeveralProviders(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	// The premise: an id whose FIRST catalogue match is a different provider,
	// which is also a real model under the one we are asking for. If the
	// catalogue is ever reordered so this is no longer true, the test is
	// vacuous rather than wrong — say so instead of passing quietly.
	const id = "gpt-5-pro"
	bare, err := provider.FindModel("", id)
	if err != nil {
		t.Skipf("%s is no longer in the catalogue", id)
	}
	if _, err := provider.FindModel("openai", id); err != nil {
		t.Skipf("%s is no longer an openai model", id)
	}
	if bare.Provider == "openai" {
		t.Skipf("%s now resolves to openai bare; the collision this guards is gone", id)
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
