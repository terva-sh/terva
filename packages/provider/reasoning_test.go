package provider

import "testing"

func TestReasoningEffortMappings(t *testing.T) {
	cases := []struct {
		level      string
		model      string // only affects the codex mapping (native max is model-gated)
		openai     string
		anthCompat string
		codex      string
		budget     int
		normalized string
	}{
		{"off", "gpt-5.6-sol", "", "", "", 0, ""},
		{"minimum", "gpt-5.6-sol", "low", "low", "low", 1024, "minimum"},
		{"minimal", "gpt-5.6-sol", "low", "low", "low", 1024, "minimum"},
		{"low", "gpt-5.6-sol", "low", "low", "low", 2048, "low"},
		{"medium", "gpt-5.6-sol", "medium", "medium", "medium", 8192, "medium"},
		{"high", "gpt-5.6-sol", "high", "high", "high", 16384, "high"},
		{"maximum", "gpt-5.6-sol", "high", "xhigh", "xhigh", 32768, "maximum"},
		// max is a separate tier above maximum: native on GPT-5.6 and adaptive
		// Claude (anthCompat), clamped to xhigh on other codex models.
		{"max", "gpt-5.6-sol", "high", "max", "max", 32768, "max"},
		{"max", "gpt-5.5", "high", "max", "xhigh", 32768, "max"},
	}
	for _, tc := range cases {
		if got := NormalizeReasoning(tc.level); got != tc.normalized {
			t.Errorf("NormalizeReasoning(%q)=%q want %q", tc.level, got, tc.normalized)
		}
		if got := OpenAIReasoningEffort(tc.level); got != tc.openai {
			t.Errorf("OpenAIReasoningEffort(%q)=%q want %q", tc.level, got, tc.openai)
		}
		if got := OpenAICompatAnthropicEffort(tc.level); got != tc.anthCompat {
			t.Errorf("OpenAICompatAnthropicEffort(%q)=%q want %q", tc.level, got, tc.anthCompat)
		}
		if got := OpenAICodexReasoningEffort(tc.level, tc.model); got != tc.codex {
			t.Errorf("OpenAICodexReasoningEffort(%q, %q)=%q want %q", tc.level, tc.model, got, tc.codex)
		}
		if got := ReasoningBudget(tc.level); got != tc.budget {
			t.Errorf("ReasoningBudget(%q)=%d want %d", tc.level, got, tc.budget)
		}
	}
}
