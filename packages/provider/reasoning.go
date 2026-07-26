package provider

import "strings"

// NormalizeReasoning canonicalizes terva's user-facing thinking levels.
// Empty string means reasoning/thinking is disabled. "maximum" is the
// long-standing top tier (mapped to xhigh effort); "max" is a separate
// opt-in tier above it, sent natively only to models that support it
// (GPT-5.6, adaptive Claude) and clamped to the "maximum" effort elsewhere.
func NormalizeReasoning(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "off", "none", "no", "false", "disabled":
		return ""
	case "min", "minimal", "minimum":
		return "minimum"
	case "low":
		return "low"
	case "med", "medium":
		return "medium"
	case "hi", "high":
		return "high"
	case "maximum":
		return "maximum"
	case "max":
		return "max"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

// NormalizeReasoningSummary canonicalizes the reasoning-summary setting onto
// the values the OpenAI Responses backend accepts, and returns "" (off) for
// anything it does not recognize.
//
// Unknown values are dropped rather than forwarded on purpose: this field
// rides every request on the path, so a typo'd config would otherwise turn
// into a 400 on every single turn instead of a silently absent summary.
// Failing off degrades to today's behavior; failing open breaks the session.
func NormalizeReasoningSummary(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "on", "true", "enabled":
		return "auto"
	case "concise", "short":
		return "concise"
	case "detailed", "full", "long":
		return "detailed"
	default:
		return ""
	}
}

// EffectiveReasoning resolves the reasoning level for a turn against model m: an
// explicitly-set global level (reasoningSet==true, incl. "" meaning the user
// chose off) wins; otherwise the model's DefaultReasoning applies; otherwise
// off. The result is NORMALIZED, so callers gate on `!= ""` and pass it to the
// budget/effort mappers unchanged. Single home of the global-vs-model precedence,
// shared by every backend's request builder.
func EffectiveReasoning(reqReasoning string, reasoningSet bool, m Model) string {
	if reasoningSet {
		return NormalizeReasoning(reqReasoning)
	}
	return NormalizeReasoning(m.DefaultReasoning)
}

// ReasoningBudget returns terva's approximate token budget for thinking-capable
// providers that accept explicit budgets.
func ReasoningBudget(level string) int {
	switch NormalizeReasoning(level) {
	case "minimum":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	case "maximum", "max":
		return 32768
	default:
		return 0
	}
}

// AnthropicAdaptiveEffort maps terva's user-facing thinking levels onto the
// effort enum used by Anthropic's adaptive-thinking models (Opus 4.7+).
// These models reject explicit thinking budgets; thinking depth is
// controlled by output_config.effort instead. Returns "" when reasoning
// is disabled.
func AnthropicAdaptiveEffort(level string) string {
	switch NormalizeReasoning(level) {
	case "minimum", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "maximum":
		return "xhigh"
	case "max":
		// Adaptive models accept a native max effort above xhigh.
		return "max"
	default:
		return ""
	}
}

// OpenAIReasoningEffort maps terva's six-level setting onto the effort enum
// accepted by OpenAI-compatible chat-completions endpoints.
func OpenAIReasoningEffort(level string) string {
	switch NormalizeReasoning(level) {
	case "minimum", "low":
		// Many OpenAI-compatible endpoints only accept low/medium/high.
		// Use low for terva's minimum instead of the newer minimal enum.
		return "low"
	case "medium":
		return "medium"
	case "high", "maximum", "max":
		// Generic compatible endpoints top out at high; clamp both top tiers.
		return "high"
	default:
		return ""
	}
}

// OpenAICompatAnthropicEffort maps terva's user-facing thinking levels
// onto reasoning_effort values when an adaptive-thinking Anthropic
// model (Opus 4.7+) is served over the OpenAI-compatible chat-
// completions wire (openrouter, opencode, ...). Differs from
// OpenAIReasoningEffort only at the top: terva's "maximum" maps to
// "xhigh" instead of being clamped to "high", so the model's full
// adaptive-thinking ceiling is preserved when reachable through a
// gateway that accepts the effort knob.
func OpenAICompatAnthropicEffort(level string) string {
	switch NormalizeReasoning(level) {
	case "minimum", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "maximum":
		return "xhigh"
	case "max":
		return "max"
	default:
		return ""
	}
}

// OpenAICodexReasoningEffort maps terva levels onto the ChatGPT/Codex
// Responses backend enum. That backend rejects "minimal" and uses
// "xhigh" for the top of the GPT-5.x tier. GPT-5.6 additionally supports
// a native "max" effort above xhigh; other models clamp "max" to xhigh.
func OpenAICodexReasoningEffort(level, model string) string {
	switch NormalizeReasoning(level) {
	case "minimum", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "maximum":
		return "xhigh"
	case "max":
		if strings.HasPrefix(strings.ToLower(model), "gpt-5.6-") {
			return "max"
		}
		return "xhigh"
	default:
		return ""
	}
}
