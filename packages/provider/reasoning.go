package provider

import "strings"

// ReasoningLevels is the ladder as a user types it, lowest to highest. It is
// the ONE source every surface that prints the ladder reads from.
//
// It exists because the three places that spoke about the ladder drifted: the
// flag accepted "max" while both `--help` and the error a typo produced listed
// only up to "maximum", so the tier that unlocks gpt-5.6's native ceiling was
// enforced but never advertised. Printing a hand-written copy of this list is
// how that happens, so there is no hand-written copy any more.
//
// Aliases ("min", "minimal", "hi", "none", …) are deliberately absent: they are
// accepted by NormalizeReasoning but are not what a surface should teach.
var ReasoningLevels = []string{"off", "minimum", "low", "medium", "high", "maximum", "max"}

// ReasoningLadder renders the ladder for help text and error messages.
func ReasoningLadder() string { return strings.Join(ReasoningLevels, "|") }

// MaxIsNative reports whether the "max" rung reaches m as a native max effort
// rather than being clamped down to the "maximum" tier.
//
// It lives beside the mappers that do the clamping (OpenAICodexReasoningEffort
// gates on the gpt-5.6 prefix; AnthropicAdaptiveEffort and
// OpenAICompatAnthropicEffort pass "max" through for adaptive models) so a
// surface that EXPLAINS the rung and the code that ENFORCES it cannot drift
// apart — which is the same failure the ladder itself just had.
func MaxIsNative(m Model) bool {
	return m.AdaptiveThinking || strings.HasPrefix(strings.ToLower(m.ID), "gpt-5.6-")
}

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
// explicitly-set level (reasoningSet==true, incl. "" meaning the user chose off)
// wins; otherwise the model's DefaultReasoning applies; otherwise off. The
// result is NORMALIZED, so callers gate on `!= ""` and pass it to the
// budget/effort mappers unchanged. Shared by every backend's request builder.
//
// This is the BOTTOM of the chain, not all of it. reqReasoning arrives already
// resolved across the layers that need to be told apart — the flag / session
// override, an operator's per-model models.json value, and the global config —
// because only the builder can distinguish them (see build.Resolve). What
// remains here is the last rung: a model's CATALOG default, which applies when
// the user chose nothing at all.
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

// ---- What a rung actually does, per model ----

// ReasoningEffect is what one ladder rung actually becomes on the wire for a
// given model: a thinking-token budget, an effort/level enum, or nothing.
//
// It exists because a rung's NAME is not its meaning. terva's ladder is one
// list for every backend, but the backends do not agree: Anthropic takes a
// token budget clamped by the model's output cap, Gemini 2.5 takes a budget
// with a per-model ceiling while Gemini 3 takes an enum, the Codex/Responses
// route takes an effort enum with no budget at all, and generic
// OpenAI-compatible endpoints collapse six rungs onto three efforts.
//
// So a surface that prints "~32k tokens of thinking" for every model is wrong
// on most of them, and silently: the request carries something else entirely.
//
// Comparable on purpose — a caller can spot two rungs that land on the same
// wire value and say so, rather than offering the user a choice that is not
// one.
type ReasoningEffect struct {
	// Budget is the thinking-token budget actually sent, or 0 when this
	// backend takes no budget.
	Budget int
	// Effort is the effort/level enum actually sent ("low", "HIGH", …), or ""
	// when this backend takes no effort knob.
	Effort string
	// Supported is false when the model accepts no reasoning control at all,
	// which is a different statement from "this rung turns reasoning off".
	Supported bool
}

// Off reports whether this rung leaves reasoning disabled on this model.
func (e ReasoningEffect) Off() bool { return e.Supported && e.Budget == 0 && e.Effort == "" }

// ReasoningEffectFor resolves what level does to m.
//
// Every arm DELEGATES to the function the request builder itself calls —
// geminiThinkingConfig, OpenAICodexReasoningEffort, anthropicThinkingBudget,
// and the effort mappers — so this cannot describe a rung differently from the
// way it is sent. The only thing added here is the routing from a provider to
// the wire it speaks, and reasoningWireFamily is census-guarded so a new
// provider cannot slip through unclassified.
func ReasoningEffectFor(m Model, level string) ReasoningEffect {
	if !m.Reasoning {
		return ReasoningEffect{Supported: false}
	}
	lv := NormalizeReasoning(level)
	switch reasoningWireFamily(m.Provider) {
	case reasoningWireNone:
		return ReasoningEffect{Supported: false}

	case reasoningWireAnthropic:
		if usesAdaptiveThinking(m) {
			return ReasoningEffect{Effort: AnthropicAdaptiveEffort(lv), Supported: true}
		}
		return ReasoningEffect{Budget: anthropicThinkingBudget(m, lv), Supported: true}

	case reasoningWireCodex:
		return ReasoningEffect{Effort: OpenAICodexReasoningEffort(lv, m.ID), Supported: true}

	case reasoningWireGemini:
		cfg := geminiThinkingConfig(m.ID, lv)
		if cfg == nil {
			return ReasoningEffect{Supported: true}
		}
		eff := ReasoningEffect{Effort: cfg.ThinkingLevel, Supported: true}
		if cfg.ThinkingBudget != nil {
			eff.Budget = *cfg.ThinkingBudget
		}
		return eff

	default: // reasoningWireOpenAICompat
		if usesAdaptiveThinking(m) {
			return ReasoningEffect{Effort: OpenAICompatAnthropicEffort(lv), Supported: true}
		}
		return ReasoningEffect{Effort: OpenAIReasoningEffort(lv), Supported: true}
	}
}

// ReasoningRung is one row of the ladder as it applies to ONE model: the rung
// a user picks, what it becomes on the wire, and — when several rungs land on
// the same wire value — which of them is the one worth naming.
type ReasoningRung struct {
	// Level is the rung as a user types it ("off", "minimum", …).
	Level string
	// Effect is what this rung becomes on the wire for this model.
	Effect ReasoningEffect
	// SameAs names the rung this one collapses onto, or "" when this rung is
	// the canonical one for its wire value. It is what stops a picker from
	// offering four rungs that are, on this model, a single choice.
	SameAs string
}

// ReasoningLadderFor builds the whole ladder for m with the collapse
// annotations resolved — everything a surface needs to explain the ladder,
// computed once here rather than in each frontend.
//
// It returns nil when m accepts no reasoning control at all. That is a
// DIFFERENT answer from a ladder whose rungs are all off: "this model takes no
// thinking setting" versus "you have chosen not to think", and a client that
// conflated them would tell a Bedrock user their model was merely switched off.
//
// The canonical rung is not simply the first: when minimum and low both send
// effort "low", the rung a user recognizes is low, and annotating THAT one as
// "same as minimum" reads backwards. So the canonical rung is the one whose
// NAME matches the wire value, and only where no name matches does ladder
// order decide.
func ReasoningLadderFor(m Model) []ReasoningRung {
	effects := make([]ReasoningEffect, len(ReasoningLevels))
	canonical := map[ReasoningEffect]string{}
	supported := false
	for i, lv := range ReasoningLevels {
		e := ReasoningEffectFor(m, LadderWireValue(lv))
		effects[i] = e
		if e.Supported {
			supported = true
		}
		if !e.Supported || e.Off() {
			continue
		}
		cur, seen := canonical[e]
		switch {
		case !seen:
			canonical[e] = lv
		case strings.EqualFold(lv, e.Effort) && !strings.EqualFold(cur, e.Effort):
			canonical[e] = lv
		}
	}
	if !supported {
		return nil
	}

	out := make([]ReasoningRung, 0, len(ReasoningLevels))
	for i, lv := range ReasoningLevels {
		r := ReasoningRung{Level: lv, Effect: effects[i]}
		if effects[i].Supported && !effects[i].Off() {
			if c := canonical[effects[i]]; c != lv {
				r.SameAs = c
			}
		}
		out = append(out, r)
	}
	return out
}

// LadderWireValue turns a displayed rung into the value the mappers take. The
// ladder prints "off" where the wire means "no reasoning", which is the empty
// string everywhere else in this package.
func LadderWireValue(rung string) string {
	if rung == "off" {
		return ""
	}
	return rung
}

type reasoningWire int

const (
	reasoningWireOpenAICompat reasoningWire = iota // chat-completions reasoning_effort
	reasoningWireAnthropic                         // thinking budget, or adaptive effort
	reasoningWireCodex                             // Responses-route effort enum
	reasoningWireGemini                            // thinkingBudget or thinkingLevel
	reasoningWireNone                              // sends no reasoning control
)

// reasoningWireWiring maps a provider id to the wire its client speaks. It is
// keyed on the provider because that is what decides which client is built
// (see packages/agent/build/provider_registry.go), and the mapping is NOT
// guessable from the name: minimax, minimax-cn and fireworks all reach
// Anthropic-wire clients, and openai-responses reaches the Codex one.
//
// Providers absent from this table fall through to the OpenAI-compatible wire,
// which is the correct default for the great majority. The census guard in
// reasoning_wire_census_test.go is what keeps that default from being a silent
// wrong answer for a newly added provider.
var reasoningWireWiring = map[string]reasoningWire{
	"anthropic":  reasoningWireAnthropic,
	"minimax":    reasoningWireAnthropic,
	"minimax-cn": reasoningWireAnthropic,
	"fireworks":  reasoningWireAnthropic,

	"openai-codex":           reasoningWireCodex,
	"openai-responses":       reasoningWireCodex,
	"azure-openai-responses": reasoningWireCodex,

	"google":        reasoningWireGemini,
	"google-vertex": reasoningWireGemini,

	"amazon-bedrock": reasoningWireNone,
}

func reasoningWireFamily(provider string) reasoningWire {
	if w, ok := reasoningWireWiring[provider]; ok {
		return w
	}
	return reasoningWireOpenAICompat
}
