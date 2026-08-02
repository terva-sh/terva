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
		// forbidSub is for the family pairs where one name CONTAINS the other:
		// asserting "gemini-2.5-flash" alone would pass on flash-lite too, so
		// the case that exists to prove they are separated must say so.
		forbidSub string
	}{
		{"weak from a sonnet host", "anthropic", "claude-sonnet-4-5", "weak", "haiku", ""},
		{"medium from an opus host", "anthropic", "claude-opus-4-5", "medium", "sonnet", ""},
		{"strong capped at a sonnet host", "anthropic", "claude-sonnet-4-5", "strong", "sonnet", ""},
		{"strong capped at a haiku host", "anthropic", "claude-haiku-4-5", "strong", "haiku", ""},
		{"unknown tier resolves to nothing", "anthropic", "claude-sonnet-4-5", "bogus", "", ""},
		{"provider without a table is a no-op", "xai", "grok-4.3", "weak", "", ""},
		{"empty tier is a no-op", "anthropic", "claude-sonnet-4-5", "", "", ""},

		// The rule that kept google out of the table until `unless` existed:
		// flash-lite CONTAINS flash, so a bare keyword would answer the
		// medium tier with the weak model and nothing would say so.
		{"gemini weak is the lite model", "google", "gemini-2.5-pro", "weak", "flash-lite", ""},
		{"gemini medium is flash, not flash-lite", "google", "gemini-2.5-pro", "medium", "flash", "lite"},
		{"gemini strong is pro", "google", "gemini-2.5-pro", "strong", "pro", "flash"},

		// openai's strong rung is defined by exclusion — "gpt-5" is a
		// substring of every mini/nano/chat/codex/pro variant.
		{"openai weak", "openai", "gpt-5", "weak", "nano", ""},
		{"openai medium", "openai", "gpt-5", "medium", "mini", "nano"},
		{"openai strong is the plain flagship", "openai", "gpt-5", "strong", "gpt-5", "mini"},
		{"openai strong capped at a mini host", "openai", "gpt-5-mini", "strong", "mini", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSwarmTier(tc.provider, tc.hostModel, tc.tier, nil).Model
			if tc.wantSub == "" {
				if got != "" {
					t.Errorf("ResolveSwarmTier(%q,%q,%q) = %q, want \"\"", tc.provider, tc.hostModel, tc.tier, got)
				}
				return
			}
			if !strings.Contains(strings.ToLower(got), tc.wantSub) {
				t.Errorf("ResolveSwarmTier(%q,%q,%q) = %q, want a %q model", tc.provider, tc.hostModel, tc.tier, got, tc.wantSub)
			}
			if tc.forbidSub != "" && strings.Contains(strings.ToLower(got), tc.forbidSub) {
				t.Errorf("ResolveSwarmTier(%q,%q,%q) = %q, which is a %q model — the rungs are not separated", tc.provider, tc.hostModel, tc.tier, got, tc.forbidSub)
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
		"opencode-go": {"weak": {Model: "minimax-m3"}, "medium": {Model: "glm-5.2"}, "strong": {Model: "kimi-k2.7-code"}},
		// Partial override of a built-in provider: only weak is pinned.
		"anthropic": {"weak": {Model: "claude-haiku-4-5"}},
	}

	// Gateway: exact override model is returned, exactly as configured.
	if got := ResolveSwarmTier("opencode-go", "kimi-k2.7-code", "weak", ov).Model; got != "minimax-m3" {
		t.Errorf("gateway weak = %q, want minimax-m3", got)
	}
	// Gateway cap: a medium host can't spawn strong (host ranked via override ids).
	if got := ResolveSwarmTier("opencode-go", "glm-5.2", "strong", ov).Model; got != "glm-5.2" {
		t.Errorf("gateway strong capped at medium host = %q, want glm-5.2", got)
	}
	// Gateway without an override entry still no-ops (no built-in table).
	if got := ResolveSwarmTier("openrouter", "anything", "weak", ov).Model; got != "" {
		t.Errorf("unconfigured gateway = %q, want \"\"", got)
	}
	// Partial override: pinned weak wins…
	if got := ResolveSwarmTier("anthropic", "claude-opus-4-5", "weak", ov).Model; got != "claude-haiku-4-5" {
		t.Errorf("anthropic weak override = %q, want claude-haiku-4-5", got)
	}
	// …while the unset medium falls back to the built-in family guess.
	if got := ResolveSwarmTier("anthropic", "claude-opus-4-5", "medium", ov).Model; !strings.Contains(got, "sonnet") {
		t.Errorf("anthropic medium fallback = %q, want a sonnet model", got)
	}
}
