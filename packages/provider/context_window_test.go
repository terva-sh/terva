package provider

import "testing"

func TestEffectiveContextWindow(t *testing.T) {
	cases := []struct {
		name    string
		max     int
		desired int
		want    int
	}{
		{"unset uses max", 1050000, 0, 1050000},
		{"desired below max", 1050000, 272000, 272000},
		{"desired equals max", 1050000, 1050000, 1050000},
		{"desired above max clamps to max", 200000, 500000, 200000},
		{"desired honored when max unknown", 0, 272000, 272000},
		{"both zero", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{ContextWindow: tc.max, DesiredContextWindow: tc.desired}
			if got := m.EffectiveContextWindow(); got != tc.want {
				t.Fatalf("EffectiveContextWindow() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGPT56CatalogEntries(t *testing.T) {
	cases := []struct {
		id         string
		cacheWrite float64
	}{
		{"gpt-5.6-sol", 6.25},
		{"gpt-5.6-terra", 3.125},
		{"gpt-5.6-luna", 1.25},
	}
	for _, tc := range cases {
		m, err := FindModel("openai-codex", tc.id)
		if err != nil {
			t.Fatalf("FindModel %s: %v", tc.id, err)
		}
		// The true model window is 1.05M (authoritative OpenAI API docs);
		// 272K is the long-context pricing-surcharge breakpoint, shipped as
		// the cost-safe default *working* window, not the context window.
		if m.ContextWindow != 1050000 {
			t.Errorf("%s ContextWindow = %d, want 1050000 (model max)", tc.id, m.ContextWindow)
		}
		if m.DesiredContextWindow != 272000 {
			t.Errorf("%s DesiredContextWindow = %d, want 272000", tc.id, m.DesiredContextWindow)
		}
		if m.ContextSurchargeAt != 272000 {
			t.Errorf("%s ContextSurchargeAt = %d, want 272000", tc.id, m.ContextSurchargeAt)
		}
		if m.EffectiveContextWindow() != 272000 {
			t.Errorf("%s EffectiveContextWindow = %d, want 272000", tc.id, m.EffectiveContextWindow())
		}
		if m.PriceCacheWrite != tc.cacheWrite {
			t.Errorf("%s PriceCacheWrite = %g, want %g (1.25x input)", tc.id, m.PriceCacheWrite, tc.cacheWrite)
		}
		if m.MaxOutput != 128000 || !m.Reasoning {
			t.Errorf("%s limits: MaxOutput=%d Reasoning=%v", tc.id, m.MaxOutput, m.Reasoning)
		}
	}
}

func TestDesiredContextWindowUserOverride(t *testing.T) {
	withCatalogState(t)

	// A user raises the shipped cost-safe 272K working window toward the
	// model's true 1.05M max for gpt-5.6-sol via models.json.
	SetUserModels([]Model{{
		Provider:             "openai-codex",
		ID:                   "gpt-5.6-sol",
		DesiredContextWindow: 500000,
		Source:               "user",
	}})

	got, err := FindModel("openai-codex", "gpt-5.6-sol")
	if err != nil {
		t.Fatalf("FindModel: %v", err)
	}
	if got.DesiredContextWindow != 500000 {
		t.Fatalf("DesiredContextWindow = %d, want 500000 (user override)", got.DesiredContextWindow)
	}
	if got.ContextWindow != 1050000 {
		t.Fatalf("ContextWindow = %d, want 1050000 (model max unchanged by desired override)", got.ContextWindow)
	}
	if got.EffectiveContextWindow() != 500000 {
		t.Fatalf("EffectiveContextWindow = %d, want 500000", got.EffectiveContextWindow())
	}
}
