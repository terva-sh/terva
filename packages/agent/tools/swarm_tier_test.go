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
			got := ResolveSwarmTier(tc.provider, tc.hostModel, tc.tier)
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
