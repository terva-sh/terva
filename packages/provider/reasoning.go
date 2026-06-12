package provider

import "strings"

// NormalizeReasoning canonicalizes terva's user-facing thinking levels.
// Empty string means reasoning/thinking is disabled.
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
	case "max", "maximum":
		return "maximum"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
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
	case "maximum":
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
	case "high", "maximum":
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
	default:
		return ""
	}
}

// OpenAICodexReasoningEffort maps terva levels onto the ChatGPT/Codex
// Responses backend enum. That backend rejects "minimal" and uses
// "xhigh" for the highest tier on recent GPT-5.x models.
func OpenAICodexReasoningEffort(level string) string {
	switch NormalizeReasoning(level) {
	case "minimum", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "maximum":
		return "xhigh"
	default:
		return ""
	}
}
