package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// swapFakeClient is a no-op client; these tests exercise only the model-swap
// bookkeeping on Agent, not any request round-trip.
type swapFakeClient struct{ name string }

func (c *swapFakeClient) Name() string { return c.name }
func (c *swapFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	return nil, nil
}

// TestSetModelRefreshesMaxTokens: a /model swap must re-derive Agent.MaxTokens
// from the target model's MaxOutput. MaxTokens is seeded once at build time
// from the launch model; without the refresh a swap to a lower-cap model
// leaves the field pinned to the old (larger) budget, which then misreports in
// cost/context accounting and terva_status.
func TestSetModelRefreshesMaxTokens(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "big-cap", DisplayName: "Big", ContextWindow: 200000, MaxOutput: 128000, Source: "user"},
		{Provider: "anthropic", ID: "small-cap", DisplayName: "Small", ContextWindow: 200000, MaxOutput: 64000, Source: "user"},
		{Provider: "anthropic", ID: "no-cap", DisplayName: "NoCap", ContextWindow: 200000, MaxOutput: 0, Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	a := &Agent{Model: "big-cap", MaxTokens: 128000}

	// Swap down to the lower-cap model: MaxTokens must follow.
	a.SetModel("small-cap")
	if a.MaxTokens != 64000 {
		t.Fatalf("after swap to small-cap, MaxTokens = %d; want 64000", a.MaxTokens)
	}

	// Swap back up: MaxTokens must grow again.
	a.SetModel("big-cap")
	if a.MaxTokens != 128000 {
		t.Fatalf("after swap back to big-cap, MaxTokens = %d; want 128000", a.MaxTokens)
	}
}

// TestSetModelRefreshBestEffort: swapping to a model that is absent from the
// catalog, or that advertises no output cap (MaxOutput 0), must leave the
// existing working budget untouched — never zero it, which would drop the
// provider default (e.g. Bedrock's 4096) and truncate long turns.
func TestSetModelRefreshBestEffort(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "no-cap", DisplayName: "NoCap", ContextWindow: 200000, MaxOutput: 0, Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	a := &Agent{Model: "big-cap", MaxTokens: 128000}

	// Unknown id: keep the previous budget.
	a.SetModel("totally-unknown-model")
	if a.MaxTokens != 128000 {
		t.Fatalf("unknown-model swap zeroed/changed MaxTokens to %d; want 128000 preserved", a.MaxTokens)
	}

	// Known id but no advertised cap: still preserve.
	a.SetModel("no-cap")
	if a.MaxTokens != 128000 {
		t.Fatalf("no-cap swap changed MaxTokens to %d; want 128000 preserved", a.MaxTokens)
	}
}

// TestSetClientAndModelRefreshesMaxTokens: the cross-endpoint rebuild path
// (SetClientAndModel) must refresh the budget too, so a same-transcript swap to
// a different provider/model doesn't carry the old cap.
func TestSetClientAndModelRefreshesMaxTokens(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "big-cap", DisplayName: "Big", ContextWindow: 200000, MaxOutput: 128000, Source: "user"},
		{Provider: "anthropic", ID: "small-cap", DisplayName: "Small", ContextWindow: 200000, MaxOutput: 64000, Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	a := &Agent{Model: "big-cap", MaxTokens: 128000}
	a.SetClientAndModel(&swapFakeClient{name: "anthropic"}, "small-cap")
	if a.MaxTokens != 64000 {
		t.Fatalf("after SetClientAndModel to small-cap, MaxTokens = %d; want 64000", a.MaxTokens)
	}
}
