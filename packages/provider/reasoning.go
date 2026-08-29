package provider

import "strings"

// ReasoningLevels is the ladder as a user types it, lowest to highest. It is
// the ONE source every surface that prints the ladder reads from.
//
// It exists because the three places that spoke about the ladder drifted: the
// flag accepted "max" while both `--help` and the error a typo produced listed
// only up to "maximum", so the tier that unlocks gpt-5.6's native ceiling was
// enforced but never advertised. Printing a hand-written copy of this list is
// how that happens, so there is no hand-written copy in Go.
//
// There IS one more, and it cannot be removed: the web client's REASONING_LEVELS
// (ui/ReasoningPick.tsx) is a different language and cannot import this. That
// copy is held to this one by reasoning-ladder-parity.test.ts, which reads this
// var out of this file. Until that guard existed the claim above was simply
// false for the web surface, in the exact way it describes having already
// happened once.
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

// ReasoningSource names the layer of the chain that decided a level.
//
// It exists because "which one won" is a different question from "what is the
// level", and every surface that explains the setting to a user needs the
// first. Without it a surface can only re-derive the answer from the raw
// inputs, and the tree had five doing exactly that — all wrong the same way.
//
// Deliberately NOT called a rung. In this package a ReasoningRung is already
// one row of the LEVEL ladder ("off" … "max") as it applies to one model, and
// the two ideas are perpendicular: a level says how hard to think, a source
// says who chose it. Reusing the word is how you end up with the drift this
// symbol exists to end. See ResolveReasoning.
type ReasoningSource int

const (
	// ReasoningFromSession is the --reasoning flag or a session override.
	ReasoningFromSession ReasoningSource = iota
	// ReasoningFromModelOperator is an operator's per-model models.json
	// `defaultReasoning`. It sits ABOVE the global setting: it is a choice
	// someone made about this model specifically.
	ReasoningFromModelOperator
	// ReasoningFromGlobal is the global config setting.
	ReasoningFromGlobal
	// ReasoningFromModelCatalog is the model's catalog DefaultReasoning. It
	// sits BELOW the global setting: it is a fallback shipped with the row,
	// meant to yield to anything the user actually chose.
	ReasoningFromModelCatalog
	// ReasoningFromNothing is nothing set anywhere — the chain runs out.
	ReasoningFromNothing
)

// ResolveReasoning composes the WHOLE reasoning chain and reports which layer
// decided it:
//
//	--reasoning / session > models.json per-model > global config > CATALOG default > off
//
// Until this existed the chain was composed nowhere in production. The turn
// path walked it in two halves that could not see each other — build.Resolve
// handled the first three rungs, EffectiveReasoning the last two — and the only
// thing that ever joined them was a helper inside a test. Every display surface
// therefore re-derived it by hand from (global, model.DefaultReasoning), and
// every one of them made the same mistake: with no way to tell an operator's
// per-model value from a catalog default, they collapsed the two model rungs
// into one and put it BELOW the global.
//
// What that costs the operator: they set `defaultReasoning` for a model in
// models.json, and the dialog tells them the session will "follow the global
// setting" — naming a value that is not deciding anything. The turn then runs
// at their per-model level, so the surface and the behaviour disagree.
//
// 🪤 The two model rungs are the SAME FIELD (DefaultReasoning) on opposite
// sides of the global, told apart only by DefaultReasoningSet. Reading the raw
// field without the set-signal makes a global "low" unreachable on every k3
// row (they carry a catalog default so the endpoint stops downgrading to K2).
//
// Precedence is decided on the RAW strings, before normalizing: a non-empty raw
// level — including "off"/"none", which normalize to "" — is an explicit
// choice, and must beat the rungs below it. The returned level IS normalized.
func ResolveReasoning(session string, m Model, global string) (level string, from ReasoningSource) {
	if session != "" {
		return NormalizeReasoning(session), ReasoningFromSession
	}
	if m.DefaultReasoningSet && m.DefaultReasoning != "" {
		return NormalizeReasoning(m.DefaultReasoning), ReasoningFromModelOperator
	}
	if global != "" {
		return NormalizeReasoning(global), ReasoningFromGlobal
	}
	if m.DefaultReasoning != "" {
		return NormalizeReasoning(m.DefaultReasoning), ReasoningFromModelCatalog
	}
	return "", ReasoningFromNothing
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

// openAICompatEffort is the ONE decision for the chat-completions
// reasoning_effort knob: given a model and a level, what goes on the wire.
// An empty result means send no reasoning_effort at all.
//
// The wire and the ladder both call it, so the dialog cannot describe a rung
// differently from the way the request sends it.
//
// 🪤 openaiClient.buildRequest must call THIS, not ReasoningEffectFor.
// ReasoningEffectFor picks its arm from m.Provider, and buildRequest resolves
// its model with FindModel("", id) — a lookup that ignores the requesting
// provider on purpose, so local and custom endpoints work without a catalog
// row of their own. The provider on the row it finds is therefore not
// reliably the provider making the request. Routing on it would let a row
// belonging to an Anthropic-wire provider answer with a thinking budget and
// an empty effort, and this client would silently stop sending the knob.
// The client already knows its wire, because it IS this wire, so it asks for
// this arm by name instead of being routed to it.
func openAICompatEffort(m Model, level string) string {
	lv := NormalizeReasoning(level)
	if len(m.ReasoningEfforts) > 0 {
		// The model has told us its enum, so aim at what the rung MEANS and
		// bend it onto that set. The conservative mappers below must not run
		// first: they clamp "maximum" to "high" because an unknown server
		// might reject xhigh, and on a model declaring {none, low, medium,
		// xhigh} that pre-clamp lands "maximum" on medium — the cheaper
		// neighbour of a rung the model never had — while the xhigh it
		// actually wanted sat declared and unused.
		return clampEffortToDeclared(m.ReasoningEfforts, idealCompatEffort(lv))
	}
	if usesAdaptiveThinking(m) {
		// Some gateways expose adaptive-thinking Anthropic models through
		// the OpenAI-compatible chat-completions wire. They accept the
		// same reasoning_effort knob, including the top "xhigh" tier;
		// don't clamp terva's "maximum" to "high" for those models.
		return OpenAICompatAnthropicEffort(lv)
	}
	return OpenAIReasoningEffort(lv)
}

// idealCompatEffort is terva's rung said plainly on the chat-wire scale, with
// none of the defensive clamping the blind mappers need.
//
// It is only ever used for a model that declares its efforts. That is what
// makes honesty safe here: OpenAIReasoningEffort turns "minimum" into "low"
// and both top rungs into "high" because it is guessing at a server it cannot
// interrogate, and a wrong guess is an HTTP 400 on every turn. A declared set
// removes the guess, so the rung can mean what it says and be bent afterwards
// onto something the model admits to.
func idealCompatEffort(level string) string {
	switch level {
	case "":
		return "none"
	case "minimum":
		return "minimal"
	case "maximum":
		return "xhigh"
	case "low", "medium", "high", "max":
		return level
	default:
		// An unrecognized level is a server's own word. Pass it through and
		// let the clamp leave it alone.
		return level
	}
}

// reasoningEffortScale orders the chat-wire effort values from least thinking
// to most. It is the yardstick for "nearest supported rung", nothing more: a
// value absent from this scale is passed through untouched rather than guessed
// at, because an unknown effort is a server's own extension and terva has no
// standing to move it.
var reasoningEffortScale = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// clampEffortToDeclared bends a wanted effort onto what the model says it
// accepts. declared is Model.ReasoningEfforts; empty means unlimited and the
// want is returned untouched.
//
// Two rules earn their keep:
//
//   - Off (want "") becomes "none" when the model declares that rung, and
//     stays "" otherwise. This is the only way to disable thinking on a server
//     whose default is to think — omitting the field there buys MAXIMUM
//     effort, so terva's cheapest rung was silently its most expensive.
//     A model that cannot be told "none" keeps omitting, which is honest:
//     such a model reasons whatever terva does.
//
//   - A wanted thinking level never falls back to "none". The nearest rung to
//     "low" on a model offering only {none, xhigh} is arithmetically "none",
//     and answering that would turn thinking OFF for a user who just asked to
//     think. Silence is not a degraded thought.
func clampEffortToDeclared(declared []string, want string) string {
	if len(declared) == 0 {
		return want
	}
	ok := make(map[string]bool, len(declared))
	for _, d := range declared {
		ok[strings.ToLower(strings.TrimSpace(d))] = true
	}
	// "" and "none" are the same request — do not think — and neither may
	// climb to a thinking rung. Bending "none" up to "minimal" because the
	// model lacks "none" would switch thinking ON for a user who asked for
	// off, which is the very inversion this field exists to end.
	if want == "" || want == "none" {
		if ok["none"] {
			return "none"
		}
		return ""
	}
	if ok[want] {
		return want
	}
	idx := -1
	for i, s := range reasoningEffortScale {
		if s == want {
			idx = i
			break
		}
	}
	if idx < 0 {
		return want // not a rung terva knows; leave the operator's word alone
	}
	// Walk outward from the wanted rung, preferring the cheaper side at equal
	// distance. Index 0 is "none" and is excluded: see the doc comment.
	for d := 1; d < len(reasoningEffortScale); d++ {
		if lo := idx - d; lo >= 1 && ok[reasoningEffortScale[lo]] {
			return reasoningEffortScale[lo]
		}
		if hi := idx + d; hi < len(reasoningEffortScale) && ok[reasoningEffortScale[hi]] {
			return reasoningEffortScale[hi]
		}
	}
	// The model declares no thinking rung at all. Send nothing and let the
	// server pick, rather than send a value it has told us it rejects.
	return ""
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
//
// "none" counts as off. It is the same state as sending nothing, said out
// loud: a model that declares the rung is told not to think, rather than left
// to its own default. Without this arm, declaring "none" would make the off
// rung report as a live thinking level, and the ladder would offer "off" as
// something to think with.
func (e ReasoningEffect) Off() bool {
	return e.Supported && e.Budget == 0 && (e.Effort == "" || e.Effort == "none")
}

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
		return ReasoningEffect{Effort: openAICompatEffort(m, lv), Supported: true}
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
	// reasoningWireUnknown is the zero value ON PURPOSE, and nothing may map to
	// it. A client that forgets to declare its wire must read as undeclared, not
	// as a real wire: when OpenAI-compat sat at iota 0, every silent omission
	// became a confident wrong answer, which is how vercel-ai-gateway spent its
	// whole life reporting an effort enum for an Anthropic thinking budget.
	reasoningWireUnknown      reasoningWire = iota
	reasoningWireOpenAICompat               // chat-completions reasoning_effort
	reasoningWireAnthropic                  // thinking budget, or adaptive effort
	reasoningWireCodex                      // Responses-route effort enum
	reasoningWireGemini                     // thinkingBudget or thinkingLevel
	reasoningWireNone                       // sends no reasoning control
)

// String names the wire for cross-package comparison. The build package holds
// the provider registry and must be able to check a table row against the
// client the registry actually constructs, without importing an unexported
// enum — and provider cannot import build.
func (w reasoningWire) String() string {
	switch w {
	case reasoningWireOpenAICompat:
		return "openai-compat"
	case reasoningWireAnthropic:
		return "anthropic"
	case reasoningWireCodex:
		return "codex"
	case reasoningWireGemini:
		return "gemini"
	case reasoningWireNone:
		return "none"
	default:
		return "unknown"
	}
}

// ClientReasoningWire names the reasoning wire the CLIENT actually speaks, as
// declared by the concrete client's Capabilities(). Looks through wrappers.
// Returns "unknown" for a client that has not declared one.
func ClientReasoningWire(c Client) string { return ClientCaps(c).ReasoningWire.String() }

// ProviderReasoningWire names the wire the provider TABLE believes the given
// provider id speaks. The pair (ClientReasoningWire, ProviderReasoningWire)
// must agree for every registered provider; see the build-side guard.
func ProviderReasoningWire(provider string) string { return reasoningWireFamily(provider).String() }

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
	// 🪤 kimi is Kimi CODE — Kimi behind the Anthropic Messages API at
	// api.kimi.com/coding, built by NewKimiCodingWithHeaders in every auth
	// mode. It reads like an OpenAI-compatible vendor and is not one. Absent
	// from this table it fell through to the default and /reasoning reported
	// an effort enum for k3 while the request carried
	// thinking{enabled, budget_tokens:16384} — the exact wrong answer this
	// table exists to prevent. moonshotai is the OpenAI-wire Kimi; they are
	// different providers.
	"kimi": reasoningWireAnthropic,
	// 🪤 vercel-ai-gateway is the SAME trap as kimi, and it went unfixed
	// through the kimi fix: NewVercelGatewayAnthropic is NewAnthropicCompat,
	// so the client is an anthropicClient. Absent from this table it fell
	// through to the OpenAI-compatible default, and /reasoning reported
	// "effort: medium" for a request carrying
	// thinking{enabled, budget_tokens:8192} — a field the Anthropic Messages
	// body has no slot for, and a budget the dialog never mentioned. 109
	// catalog rows carry this provider with Reasoning:true.
	"vercel-ai-gateway": reasoningWireAnthropic,

	"openai-codex":     reasoningWireCodex,
	"openai-responses": reasoningWireCodex,
	// 🪤 azure-openai-responses is NOT the Codex wire, despite the name it
	// shares with openai-responses (which genuinely is). NewAzureOpenAIResponses
	// builds an openaiClient, and azure_openai.go's own header states the Chat
	// Completions choice deliberately. Classified as Codex, the ladder offered
	// "maximum" and "high" as two rungs whose requests were byte-identical.
	// It takes the OpenAI-compat default; the row is deliberately absent.

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
