package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// The invariant this whole fix exists to hold: an endpoint id, as STORED, must
// be the same string canonicalProvider produces — because every resolve path
// asks for the canonical form and Go maps are case-sensitive.
//
// Before the fix, ValidEndpointName permitted A-Z, so "NeoT" was stored
// verbatim as the config key, the registry id, and the Provider of every model
// discovered from it, while Resolve looked up "neot". The endpoint branch
// missed, the credential lookup missed, and the fallback scan moved the
// operator to a different provider — with discovery still working, so the
// symptom read as "the model picker will not keep my choice".
func TestRegisteredEndpointIDIsCanonical(t *testing.T) {
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	// Spellings an operator can actually type into the login form.
	for _, name := range []string{"NeoT", "  NeoT  ", "NEOT", "neot"} {
		const canon = "neot"
		if err := RegisterOrReplaceEndpoint(name, config.EndpointConfig{
			BaseURL: "http://vllm.example:8000/v1",
		}); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
		t.Cleanup(func() { UnregisterEndpoint(canon) })

		if got := CanonicalEndpointID(name); got != canon {
			t.Errorf("CanonicalEndpointID(%q) = %q, want %q", name, got, canon)
		}
		// The registry must hold the canonical spelling, not what was passed.
		if !IsKnownProvider(canon) {
			t.Errorf("registered %q: IsKnownProvider(%q) = false — Resolve cannot see it", name, canon)
		}
		// And the two normalisers must agree, which is the actual invariant:
		// canonicalProvider is what Resolve applies, CanonicalEndpointID is what
		// storage applies. If these ever diverge the bug returns.
		if got := canonicalProvider(name); got != canon {
			t.Errorf("canonicalProvider(%q) = %q, want %q — storage and resolve disagree", name, got, canon)
		}
	}
}

// Self-enrolling: whatever is in the registry, however it got there, must
// satisfy the invariant. A future registration path that forgets to normalise
// fails here rather than in an operator's session.
func TestEveryEndpointInTheRegistryIsCanonical(t *testing.T) {
	if err := RegisterOrReplaceEndpoint("MixedCase.Box", config.EndpointConfig{
		BaseURL: "http://box.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint("mixedcase.box") })

	registryMu.RLock()
	ids := make([]string, 0, len(endpointProviders))
	for id := range endpointProviders {
		ids = append(ids, id)
	}
	registryMu.RUnlock()

	if len(ids) == 0 {
		t.Fatal("no endpoints registered — the scan would pass over an empty list")
	}
	for _, id := range ids {
		if got := CanonicalEndpointID(id); got != id {
			t.Errorf("endpoint registered as %q, which is not canonical (%q)", id, got)
		}
		if got := canonicalProvider(id); got != id {
			t.Errorf("endpoint %q does not survive canonicalProvider (%q) — Resolve will not find it", id, got)
		}
	}
}

// The end-to-end shape of the reported bug: a mixed-case endpoint with a
// discovered model must be selectable through the id Resolve actually uses.
func TestMixedCaseEndpointIsReachableAfterCanonicalisation(t *testing.T) {
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	const typed = "NeoT"
	if err := RegisterOrReplaceEndpoint(typed, config.EndpointConfig{
		BaseURL: "http://vllm.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(CanonicalEndpointID(typed)) })

	// Discovery files models under the id the registry holds.
	provider.RegisterExtraModel(provider.Model{
		Provider: CanonicalEndpointID(typed), ID: "qwen3.8-27b", DisplayName: "qwen3.8-27b",
		ContextWindow: 32768, MaxOutput: 4096,
	})

	resolved := canonicalProvider(typed)
	if !IsKnownProvider(resolved) {
		t.Fatalf("IsKnownProvider(%q) = false", resolved)
	}
	// A model to offer is what keeps the fallback scan from skipping straight
	// past this endpoint and switching the operator to another provider.
	if got := EndpointDefaultModel(resolved); got != "qwen3.8-27b" {
		t.Errorf("EndpointDefaultModel(%q) = %q, want qwen3.8-27b", resolved, got)
	}
	if _, err := provider.FindModel(resolved, "qwen3.8-27b"); err != nil {
		t.Errorf("FindModel(%q, qwen3.8-27b): %v", resolved, err)
	}
	// The dots in the model id are not the bug and must not become one.
	if _, err := provider.FindModel(resolved, "qwen3-8-27b"); err == nil {
		t.Error("a dash-for-dot spelling resolved — model ids must be stored verbatim")
	}
}
