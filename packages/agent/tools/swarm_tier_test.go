package tools

import (
	"strings"
	"testing"
)

func TestResolveSwarmTier(t *testing.T) {
	// The built-in catalog always carries Anthropic haiku/sonnet/opus
	// models, so these resolve without any live-model setup.
	cases := []struct {
		name      string
		provider  string
		hostModel string
		tier      string
		wantSub   string // substring the resolved id must contain; "" means resolve to ""
	}{
		{"weak from a sonnet host", "anthropic", "claude-sonnet-4-5", "weak", "haiku"},
		{"medium from an opus host", "anthropic", "claude-opus-4-5", "medium", "sonnet"},
		{"strong capped at a sonnet host", "anthropic", "claude-sonnet-4-5", "strong", "sonnet"},
		{"strong capped at a haiku host", "anthropic", "claude-haiku-4-5", "strong", "haiku"},
		{"unknown tier resolves to nothing", "anthropic", "claude-sonnet-4-5", "bogus", ""},
		{"provider without a table is a no-op", "openai", "gpt-5", "weak", ""},
		{"empty tier is a no-op", "anthropic", "claude-sonnet-4-5", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSwarmTier(tc.provider, tc.hostModel, tc.tier, nil)
			if tc.wantSub == "" {
				if got != "" {
					t.Errorf("ResolveSwarmTier(%q,%q,%q) = %q, want \"\"", tc.provider, tc.hostModel, tc.tier, got)
				}
				return
			}
			if !strings.Contains(strings.ToLower(got), tc.wantSub) {
				t.Errorf("ResolveSwarmTier(%q,%q,%q) = %q, want a %q model", tc.provider, tc.hostModel, tc.tier, got, tc.wantSub)
			}
		})
	}
}

// A user override supplies tiers for a provider with no built-in table (the
// gateway case), composes with the built-in table (partial override), and
// the host cap is computed from the override's own ids.
func TestResolveSwarmTierOverrides(t *testing.T) {
	ov := SwarmTierMap{
		// A gateway terva can't guess: full weak/medium/strong pins.
		"opencode-go": {"weak": "minimax-m3", "medium": "glm-5.2", "strong": "kimi-k2.7-code"},
		// Partial override of a built-in provider: only weak is pinned.
		"anthropic": {"weak": "claude-haiku-4-5"},
	}

	// Gateway: exact override model is returned, exactly as configured.
	if got := ResolveSwarmTier("opencode-go", "kimi-k2.7-code", "weak", ov); got != "minimax-m3" {
		t.Errorf("gateway weak = %q, want minimax-m3", got)
	}
	// Gateway cap: a medium host can't spawn strong (host ranked via override ids).
	if got := ResolveSwarmTier("opencode-go", "glm-5.2", "strong", ov); got != "glm-5.2" {
		t.Errorf("gateway strong capped at medium host = %q, want glm-5.2", got)
	}
	// Gateway without an override entry still no-ops (no built-in table).
	if got := ResolveSwarmTier("openrouter", "anything", "weak", ov); got != "" {
		t.Errorf("unconfigured gateway = %q, want \"\"", got)
	}
	// Partial override: pinned weak wins…
	if got := ResolveSwarmTier("anthropic", "claude-opus-4-5", "weak", ov); got != "claude-haiku-4-5" {
		t.Errorf("anthropic weak override = %q, want claude-haiku-4-5", got)
	}
	// …while the unset medium falls back to the built-in family guess.
	if got := ResolveSwarmTier("anthropic", "claude-opus-4-5", "medium", ov); !strings.Contains(got, "sonnet") {
		t.Errorf("anthropic medium fallback = %q, want a sonnet model", got)
	}
}
