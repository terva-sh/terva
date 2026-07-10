package build

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// Every registered spec is internally consistent and constructable.
func TestProviderRegistryWellFormed(t *testing.T) {
	for _, spec := range providerSpecs {
		if spec.id == "" {
			t.Error("spec with empty id")
		}
		if spec.newClient == nil {
			t.Errorf("%q has no newClient", spec.id)
		}
		if spec.defaultModel != "" && spec.noDefaultModel {
			t.Errorf("%q sets both defaultModel and noDefaultModel", spec.id)
		}
		if _, ok := ProviderByID[spec.id]; !ok {
			t.Errorf("%q missing from the id index", spec.id)
		}
	}
	// KnownProviders is the registry's id list: the static specs come first, in
	// order, followed by any dynamically-registered endpoints (RegisterEndpoint
	// appends — and other tests in this process may have added some).
	if len(KnownProviders) < len(providerSpecs) {
		t.Fatalf("knownProviders has %d ids, fewer than the %d static specs", len(KnownProviders), len(providerSpecs))
	}
	for i, spec := range providerSpecs {
		if KnownProviders[i] != spec.id {
			t.Errorf("knownProviders[%d] = %q, want %q (static order must match registry)", i, KnownProviders[i], spec.id)
		}
	}
}

// Every alias resolves to a real canonical id and never collides with
// a canonical id or another alias.
func TestProviderRegistryAliasesResolve(t *testing.T) {
	for alias, canon := range providerAliases {
		if !IsKnownProvider(canon) {
			t.Errorf("alias %q maps to unknown id %q", alias, canon)
		}
		if IsKnownProvider(alias) {
			t.Errorf("alias %q collides with a canonical id", alias)
		}
		if canonicalProvider(alias) != canon {
			t.Errorf("canonicalProvider(%q) = %q, want %q", alias, canonicalProvider(alias), canon)
		}
	}
}

// NewClient builds a non-nil client of the expected provider name for
// every registered provider, driving the dispatch end to end with a
// dummy credential. (Bedrock/codex/vertex/azure constructors that need
// real credentials still return a usable client object — we only
// assert non-nil and that Name() is stable where the client exposes
// the canonical id.)
func TestProviderRegistryNewClientDispatch(t *testing.T) {
	// Providers whose client.Name() is a stable, registry-matching
	// string. Others wrap or rename (kimi-coding speaks anthropic,
	// gateways relabel), so we only assert non-nil for those.
	wantName := map[string]string{
		"anthropic": "anthropic",
		"openai":    "openai",
		"deepseek":  "deepseek",
		"google":    "google",
	}
	for _, spec := range providerSpecs {
		r := Resolved{Provider: spec.id, Credential: "dummy-key", AuthMethod: "apikey"}
		c := r.NewClient()
		if c == nil {
			t.Errorf("%q: NewClient returned nil", spec.id)
			continue
		}
		if want, ok := wantName[spec.id]; ok {
			if got := c.Name(); got != want {
				t.Errorf("%q: client Name() = %q, want %q", spec.id, got, want)
			}
		}
	}
}

// DefaultModelForProvider reads the registry: explicit defaults win,
// no-default providers return empty, unknown falls back to the global.
func TestProviderRegistryDefaultModel(t *testing.T) {
	if got := DefaultModelForProvider("openai"); got != "gpt-5" {
		t.Errorf("openai default = %q, want gpt-5", got)
	}
	if got := DefaultModelForProvider("ollama"); got != "" {
		t.Errorf("ollama default = %q, want empty", got)
	}
	if got := DefaultModelForProvider("openai-compatible"); got != "" {
		t.Errorf("openai-compatible default = %q, want empty", got)
	}
	if got := DefaultModelForProvider("anthropic"); got != provider.DefaultModel.ID {
		t.Errorf("anthropic default = %q, want global %q", got, provider.DefaultModel.ID)
	}
	if got := DefaultModelForProvider("totally-unknown"); got != provider.DefaultModel.ID {
		t.Errorf("unknown default = %q, want global %q", got, provider.DefaultModel.ID)
	}
}
