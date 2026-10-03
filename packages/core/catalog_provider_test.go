package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// sharedIDCatalog lists one model id under two providers, the way the built-in
// anthropic/claude-opus-5-5 and an anthropic-compatible endpoint's discovered
// copy do. The anthropic row comes first, so a bare-id lookup finds it. The
// cpa row carries the operator's desired window and a different output cap.
func sharedIDCatalog() *provider.Registry {
	reg := provider.NewRegistry()
	reg.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "shared-id", ContextWindow: 1_000_000, MaxOutput: 128_000},
		{Provider: "cpa", ID: "shared-id", ContextWindow: 1_000_000, DesiredContextWindow: 500_000, MaxOutput: 64_000},
		{Provider: "anthropic", ID: "anthropic-only", ContextWindow: 200_000, MaxOutput: 32_000},
	})
	return reg
}

// The reported failure: an operator set desiredContextWindow 500000 on the cpa
// entry. The gauge resolved (cpa, id) and read 100% at 700k. The engine
// resolved the bare id, got the anthropic entry's 1M, and did not compact.
func TestAutoCompactionMeasuresTheProvidersEntry(t *testing.T) {
	reg := sharedIDCatalog()
	if got := reg.ContextGauge("cpa", "shared-id"); got != 500_000 {
		t.Fatalf("fixture: the gauge reads %d, want 500000", got)
	}

	a := mustNew(nil, "shared-id", WithGate(AllowAll), WithCatalog(reg), WithProvider("cpa"))
	a.mu.Lock()
	a.messages = make([]provider.Message, AutoCompactKeepTail+2)
	a.mu.Unlock()
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 700_000})

	if _, window := a.ContextUsage(); window != reg.ContextGauge("cpa", "shared-id") {
		t.Fatalf("engine window = %d, the gauge shows %d: they must read the same entry", window, reg.ContextGauge("cpa", "shared-id"))
	}
	if !a.Compaction(CompactBeforeTurn).Compact {
		t.Fatal("at 700k of a 500k window the policy did not compact")
	}
	if got := a.maxTokens; got != 64_000 {
		t.Errorf("output budget = %d, want the cpa entry's 64000", got)
	}
}

// A swap carries the provider with the client, so an agent moved from
// anthropic onto cpa reads cpa's entry from the next lookup on.
func TestSetClientAndModelMovesTheProvider(t *testing.T) {
	reg := sharedIDCatalog()
	a := mustNew(nil, "shared-id", WithGate(AllowAll), WithCatalog(reg), WithProvider("anthropic"))
	if _, window := a.ContextUsage(); window != 1_000_000 {
		t.Fatalf("before the swap, window = %d, want anthropic's 1000000", window)
	}

	a.SetClientAndModel(nil, "cpa", "shared-id")
	if a.Provider() != "cpa" {
		t.Fatalf("Provider = %q after the swap, want cpa", a.Provider())
	}
	if _, window := a.ContextUsage(); window != 500_000 {
		t.Errorf("after the swap, window = %d, want cpa's 500000", window)
	}
	if got := a.maxTokens; got != 64_000 {
		t.Errorf("after the swap, output budget = %d, want cpa's 64000", got)
	}

	// SetModel keeps the provider: it is the same-endpoint id swap.
	a.SetModel("shared-id")
	if a.Provider() != "cpa" {
		t.Errorf("SetModel changed the provider to %q", a.Provider())
	}
}

// The fallback keeps the old answer where the scope has none: an agent that
// names no provider, and a provider that does not list the model, both read
// the first entry with the id rather than no window at all.
func TestLookupFallsBackToTheBareID(t *testing.T) {
	reg := sharedIDCatalog()

	unscoped := mustNew(nil, "shared-id", WithGate(AllowAll), WithCatalog(reg))
	if _, window := unscoped.ContextUsage(); window != 1_000_000 {
		t.Errorf("no provider: window = %d, want the first entry's 1000000", window)
	}

	elsewhere := mustNew(nil, "anthropic-only", WithGate(AllowAll), WithCatalog(reg), WithProvider("cpa"))
	if _, window := elsewhere.ContextUsage(); window != 200_000 {
		t.Errorf("provider without the model: window = %d, want the anthropic entry's 200000", window)
	}
}
