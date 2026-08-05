package provider

import (
	"testing"
)

// ReasoningEffectFor exists to let a surface tell the user what a rung does.
// That is only worth anything if it matches what the request builder actually
// puts on the wire — a dialog that promises "~32k tokens of thinking" while
// the request carries 24,576 is worse than one that says nothing.
//
// So this drives the REAL builders and compares. Not a table of expected
// strings: the builders are the authority, and a test that restated their
// output would drift with them instead of catching the drift.
func TestReasoningEffectMatchesWhatTheBuilderSends(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}

	// Both sides must see the SAME model. The builders resolve the model from
	// the catalog by id, so a fabricated Model would leave the explainer
	// reading one thing and the builder another — which is how the first
	// version of this test "passed" three arms by coincidence and failed the
	// fourth for a reason that was not the code's fault.
	catalogModel := func(t *testing.T, provider, id string) Model {
		t.Helper()
		m, err := FindModel(provider, id)
		if err != nil {
			t.Fatalf("FindModel(%q, %q): %v — pick an id that is in the builtin catalog", provider, id, err)
		}
		return m
	}

	for _, tc := range []struct {
		name     string
		provider string
		id       string
		// send returns (budget, effort) actually emitted by that backend's
		// builder for this model and level.
		send func(t *testing.T, m Model, level string) (int, string)
	}{
		{
			name:     "anthropic budget",
			provider: "anthropic",
			id:       "claude-opus-4-1-20250805",
			send: func(t *testing.T, m Model, level string) (int, string) {
				c := &anthropicClient{}
				out, err := c.buildRequest(Request{
					Model: m.ID, Messages: msgs, Reasoning: level, ReasoningSet: true,
				})
				if err != nil {
					t.Fatalf("buildRequest: %v", err)
				}
				if out.Thinking == nil {
					return 0, ""
				}
				return out.Thinking.BudgetTokens, out.OutputConfig.effortOrEmpty()
			},
		},
		{
			name:     "anthropic adaptive effort",
			provider: "anthropic",
			id:       "claude-opus-4-7",
			send: func(t *testing.T, m Model, level string) (int, string) {
				c := &anthropicClient{}
				out, err := c.buildRequest(Request{
					Model: m.ID, Messages: msgs, Reasoning: level, ReasoningSet: true,
				})
				if err != nil {
					t.Fatalf("buildRequest: %v", err)
				}
				var budget int
				if out.Thinking != nil {
					budget = out.Thinking.BudgetTokens
				}
				return budget, out.OutputConfig.effortOrEmpty()
			},
		},
		{
			name:     "codex effort",
			provider: "openai-codex",
			id:       "gpt-5.6-sol",
			send: func(t *testing.T, m Model, level string) (int, string) {
				c := &codexClient{}
				out, err := c.buildRequest(Request{
					Model: m.ID, Messages: msgs, Reasoning: level, ReasoningSet: true,
				})
				if err != nil {
					t.Fatalf("buildRequest: %v", err)
				}
				if out.Reasoning == nil {
					return 0, ""
				}
				return 0, out.Reasoning.Effort
			},
		},
		{
			name:     "openai-compatible effort",
			provider: "groq",
			id:       "deepseek-r1-distill-llama-70b",
			send: func(t *testing.T, m Model, level string) (int, string) {
				c := &openaiClient{}
				out, err := c.buildRequest(Request{
					Model: m.ID, Messages: msgs, Reasoning: level, ReasoningSet: true,
				})
				if err != nil {
					t.Fatalf("buildRequest: %v", err)
				}
				return 0, out.ReasoningEffort
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := catalogModel(t, tc.provider, tc.id)
			for _, level := range ReasoningLevels {
				lv := level
				if lv == "off" {
					lv = ""
				}
				predicted := ReasoningEffectFor(model, lv)
				budget, effort := tc.send(t, model, lv)
				if predicted.Budget != budget || predicted.Effort != effort {
					t.Errorf("level %q: ReasoningEffectFor says budget=%d effort=%q, "+
						"but the builder sends budget=%d effort=%q",
						level, predicted.Budget, predicted.Effort, budget, effort)
				}
			}
		})
	}
}

// effortOrEmpty keeps the parity table readable where a backend may omit the
// whole output_config block.
func (o *anthOutputConfig) effortOrEmpty() string {
	if o == nil {
		return ""
	}
	return o.Effort
}

// The Gemini arm is separate because its builder needs a model in the catalog
// to resolve, and because it is the one backend where the SAME rung means a
// budget on one model generation and an enum on another — the case a
// single-shape explanation gets wrong.
func TestReasoningEffectMatchesGeminiThinkingConfig(t *testing.T) {
	for _, id := range []string{"gemini-3-pro-preview", "gemini-3-flash-preview"} {
		m, err := FindModel("google", id)
		if err != nil {
			t.Fatalf("FindModel(google, %q): %v", id, err)
		}
		for _, level := range ReasoningLevels {
			lv := level
			if lv == "off" {
				lv = ""
			}
			predicted := ReasoningEffectFor(m, lv)
			cfg := geminiThinkingConfig(id, lv)
			var budget int
			var effort string
			if cfg != nil {
				effort = cfg.ThinkingLevel
				if cfg.ThinkingBudget != nil {
					budget = *cfg.ThinkingBudget
				}
			}
			if predicted.Budget != budget || predicted.Effort != effort {
				t.Errorf("%s level %q: predicted budget=%d effort=%q, config has budget=%d effort=%q",
					id, level, predicted.Budget, predicted.Effort, budget, effort)
			}
		}
	}
}

// The clamp is the whole reason a per-model answer beats a per-level one:
// ReasoningBudget("maximum") is 32768 for every model, but a model whose
// output cap is smaller cannot carry that and the builder quietly sends less.
// A dialog reading ReasoningBudget alone prints the number the request does
// not contain.
func TestReasoningEffectReflectsTheOutputCapClamp(t *testing.T) {
	small := Model{Provider: "anthropic", ID: "claude-small", Reasoning: true, MaxOutput: 8192}
	got := ReasoningEffectFor(small, "maximum")
	if got.Budget >= 8192 {
		t.Fatalf("budget %d is not clamped below the model's %d output cap", got.Budget, small.MaxOutput)
	}
	if raw := ReasoningBudget("maximum"); got.Budget == raw {
		t.Fatalf("budget %d equals the unclamped ladder value — the clamp was not applied", raw)
	}
}
