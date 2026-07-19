package provider

import (
	"fmt"
	"sync"
)

// Model describes a single LLM we know about.
type Model struct {
	Provider      string // "anthropic" | "openai"
	ID            string // API id
	DisplayName   string
	ContextWindow int // model max — the hard ceiling (maxTok clamp, display)

	// DesiredContextWindow is the working window that drives auto-compaction
	// (the warn/compact thresholds are fractions of it, via
	// EffectiveContextWindow). It lets a user keep a large-window model but
	// compact earlier — e.g. to stay under a long-context pricing surcharge —
	// without pretending the model is smaller. 0 means "use ContextWindow";
	// a value above ContextWindow is clamped down. User-settable per model
	// via models.json `desiredContextWindow`.
	DesiredContextWindow int
	// ContextSurchargeAt is the input-token count above which the provider
	// charges a higher rate (OpenAI bills 2x input / 1.5x output past 272K on
	// GPT-5.6). Informational: it names the natural cost-safe value for
	// DesiredContextWindow. 0 means no surcharge tier.
	ContextSurchargeAt int

	MaxOutput int
	Reasoning bool // supports reasoning/thinking

	// AdaptiveThinking marks Anthropic models that only support the
	// adaptive thinking mode (Opus 4.7+). These reject explicit
	// thinking budgets (thinking:{type:"enabled",budget_tokens:N} -> 400)
	// and also reject non-default sampling params (temperature/top_p/
	// top_k). The Anthropic client sends thinking:{type:"adaptive"} plus
	// output_config.effort and omits temperature for these models.
	AdaptiveThinking bool

	// Prices are USD per 1M tokens.
	PriceInput      float64
	PriceOutput     float64
	PriceCacheRead  float64
	PriceCacheWrite float64

	// Speculative marks models whose ids are known from the upstream
	// vendor's CLI but not yet live on their public API. They'll 404
	// today but start working the moment the provider flips the switch.
	Speculative bool

	// BaseURL overrides the provider's default API endpoint for this
	// model. Optional; when empty the provider's default (or the
	// --base-url flag) is used. Useful for local models served by
	// ollama, vLLM, LM Studio, etc.
	BaseURL string

	// Source is where this model entry came from: "catalog" (baked in),
	// "live" (discovered via /v1/models), or "cache" (loaded from the
	// on-disk cache). Informational.
	Source string

	// Caps holds explicit capability assertions; an absent key means
	// unknown and resolves through capDefaults via Has. Treat as
	// immutable after construction — Model is copied by value
	// everywhere, so the map is shared between copies; the only writer
	// is the layer merge, which builds fresh maps (mergeCaps). All
	// reads go through Has, never the map directly. See
	// docs/plans/model-capabilities.md.
	Caps map[Capability]bool

	// Temperature is this model's default sampling temperature (0–2), set
	// via models.json. nil leaves it to the global config / provider
	// default. AdaptiveThinking models ignore it (they reject sampling
	// params). Launch resolve order: --temperature flag > per-model > global
	// config. One of the registry-driven scalar params (see ScalarParams).
	Temperature *float32
}

// EffectiveContextWindow is the window used for auto-compaction: the
// desired working window when set and sane, otherwise the model max.
// A desired window above the model max is clamped down (you cannot make
// the model hold more than it can); a desired window on a model whose max
// is unknown (0) is honored as-is. The hard ceiling — maxTok clamp and
// capability display — always uses ContextWindow; this only moves the
// warn/compact thresholds.
func (m Model) EffectiveContextWindow() int {
	if m.DesiredContextWindow > 0 {
		if m.ContextWindow > 0 && m.DesiredContextWindow > m.ContextWindow {
			return m.ContextWindow
		}
		return m.DesiredContextWindow
	}
	return m.ContextWindow
}

// Capability names one per-model feature flag. Typed string, not
// iota: the same names appear in models.json `capabilities` keys and
// in the on-disk model cache.
type Capability string

const (
	// CapImageInput marks vision models: ImageBlocks in user/tool
	// content serialize and the tool-image mirror runs. Models that
	// can't take images get image blocks dropped at serialization
	// instead of 400-bricking the session.
	CapImageInput Capability = "image-input"
	// CapImageOutput marks image-generation models. Reserved: no
	// consumer yet; tags without consumers are allowed (they're data).
	CapImageOutput Capability = "image-output"
	// CapReasoning is the query-surface alias for the legacy
	// Model.Reasoning field — Has falls back to it, so filters and
	// display treat reasoning like any other capability without
	// migrating the field's existing consumers.
	CapReasoning Capability = "reasoning"
)

// capDefaults resolves a capability nobody asserted. Every key added
// to the constants above MUST pick its default here, choosing the
// safe/common case: image-input defaults true because most modern API
// models are vision and silently dropping images for every unknown
// model would be the worse regression.
var capDefaults = map[Capability]bool{
	CapImageInput:  true,
	CapImageOutput: false,
}

// KnownCapabilities lists every capability terva understands, for
// models.json validation warnings.
func KnownCapabilities() []Capability {
	return []Capability{CapImageInput, CapImageOutput, CapReasoning}
}

// Has reports whether the model has the capability: an explicit
// assertion when present, the legacy Reasoning field for
// CapReasoning, the per-capability default otherwise. This is the
// only sanctioned read path for Caps.
func (m Model) Has(c Capability) bool {
	if v, ok := m.Caps[c]; ok {
		return v
	}
	if c == CapReasoning {
		return m.Reasoning
	}
	return capDefaults[c]
}

// mergeCaps overlays over's keys onto base, returning a fresh map
// when there is anything to overlay. Neither input is mutated: base
// frequently aliases a catalog literal or another layer's entry.
func mergeCaps(base, over map[Capability]bool) map[Capability]bool {
	if len(over) == 0 {
		return base
	}
	out := make(map[Capability]bool, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// Catalog is the hardcoded, read-only list of supported models.
// Prices are USD per 1M tokens. The list is curated to what terva's
// clients (Anthropic Messages + OpenAI Chat Completions) can actually
// talk to; models that are only reachable through the OpenAI Responses
// API (o1-pro, o3-pro, gpt-5-pro) are omitted.
var Catalog = []Model{
	// ---- Anthropic / Claude 4.x ----
	{
		Provider: "anthropic", ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5 (latest)",
		ContextWindow: 200000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
	},
	{
		Provider: "anthropic", ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1 (latest)",
		ContextWindow: 200000, MaxOutput: 32000, Reasoning: true,
		PriceInput: 15, PriceOutput: 75, PriceCacheRead: 1.5, PriceCacheWrite: 18.75,
	},
	{
		Provider: "anthropic", ID: "claude-opus-4-0", DisplayName: "Claude Opus 4 (latest)",
		ContextWindow: 200000, MaxOutput: 32000, Reasoning: true,
		PriceInput: 15, PriceOutput: 75, PriceCacheRead: 1.5, PriceCacheWrite: 18.75,
	},
	{
		Provider: "anthropic", ID: "claude-sonnet-4-0", DisplayName: "Claude Sonnet 4 (latest)",
		ContextWindow: 200000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
	},
	{
		Provider: "anthropic", ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5 (latest)",
		ContextWindow: 200000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 1, PriceOutput: 5, PriceCacheRead: 0.1, PriceCacheWrite: 1.25,
	},

	// ---- Anthropic / Claude 3.x (legacy) ----
	{
		Provider: "anthropic", ID: "claude-3-7-sonnet-20250219", DisplayName: "Claude Sonnet 3.7",
		ContextWindow: 200000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
	},
	{
		Provider: "anthropic", ID: "claude-3-5-sonnet-20241022", DisplayName: "Claude Sonnet 3.5 v2",
		ContextWindow: 200000, MaxOutput: 8192, Reasoning: false,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
	},
	{
		Provider: "anthropic", ID: "claude-3-5-haiku-latest", DisplayName: "Claude Haiku 3.5 (latest)",
		ContextWindow: 200000, MaxOutput: 8192, Reasoning: false,
		PriceInput: 0.8, PriceOutput: 4, PriceCacheRead: 0.08, PriceCacheWrite: 1,
	},
	{
		Provider: "anthropic", ID: "claude-3-opus-20240229", DisplayName: "Claude Opus 3",
		ContextWindow: 200000, MaxOutput: 4096, Reasoning: false,
		PriceInput: 15, PriceOutput: 75, PriceCacheRead: 1.5, PriceCacheWrite: 18.75,
	},

	// ---- DeepSeek ----
	// The current public DeepSeek API exposes the V4 family on
	// api.deepseek.com/v1: Pro (1.6T/49B, reasoning-heavy) and Flash
	// (284B/13B, cheaper), both 1M context, dual thinking/non-thinking
	// modes. (deepseek-chat / deepseek-reasoner retire 2026-07-24, mapping
	// to Flash's non-thinking / thinking modes.)
	//
	// CapImageInput is asserted FALSE on purpose. The V4 models are
	// natively multimodal, but the DeepSeek *API* does not expose image
	// input yet — the chat-completions endpoint has no image content type
	// and rejects multimodal parts ("unknown variant `image_url`, expected
	// `text`"). CapImageInput governs the wire, so it must track what the
	// API accepts, not the model's latent ability: terva keeps images in
	// the transcript but drops them from outgoing DeepSeek requests (switch
	// to a vision model to send them). Flip to true when DeepSeek ships a
	// vision endpoint. An earlier row optimistically marked V4 vision-
	// capable and conflated those two things (see
	// docs/plans/model-capabilities.md).
	{
		Provider: "deepseek", ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro",
		ContextWindow: 1000000, MaxOutput: 384000, Reasoning: true,
		PriceInput: 0.435, PriceOutput: 0.87, PriceCacheRead: 0.003625,
		BaseURL: "https://api.deepseek.com",
		Caps:    map[Capability]bool{CapImageInput: false},
	},
	{
		Provider: "deepseek", ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash",
		ContextWindow: 1000000, MaxOutput: 384000, Reasoning: true,
		PriceInput: 0.14, PriceOutput: 0.28, PriceCacheRead: 0.0028,
		BaseURL: "https://api.deepseek.com",
		Caps:    map[Capability]bool{CapImageInput: false},
	},

	// ---- Kimi / Kimi Code ----
	// Anthropic-messages on https://api.kimi.com/coding (no /v1 suffix;
	// the Anthropic client appends /v1/messages itself).
	{
		Provider: "kimi", ID: "kimi-for-coding", DisplayName: "Kimi For Coding",
		ContextWindow: 262144, MaxOutput: 32768, Reasoning: true,
		PriceInput: 0, PriceOutput: 0, PriceCacheRead: 0,
		BaseURL: "https://api.kimi.com/coding",
	},

	// ---- OpenAI / GPT-5 family ----
	{
		Provider: "openai", ID: "gpt-5", DisplayName: "GPT-5",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 1.25, PriceOutput: 10, PriceCacheRead: 0.125,
	},
	{
		Provider: "openai", ID: "gpt-5-mini", DisplayName: "GPT-5 Mini",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.25, PriceOutput: 2, PriceCacheRead: 0.025,
	},
	{
		Provider: "openai", ID: "gpt-5-nano", DisplayName: "GPT-5 Nano",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.05, PriceOutput: 0.4, PriceCacheRead: 0.005,
	},

	// ---- OpenAI / GPT-4.1 family ----
	{
		Provider: "openai", ID: "gpt-4.1", DisplayName: "GPT-4.1",
		ContextWindow: 1047576, MaxOutput: 32768, Reasoning: false,
		PriceInput: 2, PriceOutput: 8, PriceCacheRead: 0.5,
	},
	{
		Provider: "openai", ID: "gpt-4.1-mini", DisplayName: "GPT-4.1 mini",
		ContextWindow: 1047576, MaxOutput: 32768, Reasoning: false,
		PriceInput: 0.4, PriceOutput: 1.6, PriceCacheRead: 0.1,
	},
	{
		Provider: "openai", ID: "gpt-4.1-nano", DisplayName: "GPT-4.1 nano",
		ContextWindow: 1047576, MaxOutput: 32768, Reasoning: false,
		PriceInput: 0.1, PriceOutput: 0.4, PriceCacheRead: 0.03,
	},

	// ---- OpenAI / GPT-4o family ----
	{
		Provider: "openai", ID: "gpt-4o", DisplayName: "GPT-4o",
		ContextWindow: 128000, MaxOutput: 16384, Reasoning: false,
		PriceInput: 2.5, PriceOutput: 10, PriceCacheRead: 1.25,
	},
	{
		Provider: "openai", ID: "gpt-4o-mini", DisplayName: "GPT-4o mini",
		ContextWindow: 128000, MaxOutput: 16384, Reasoning: false,
		PriceInput: 0.15, PriceOutput: 0.6, PriceCacheRead: 0.08,
	},

	// ---- OpenAI / reasoning models ----
	{
		Provider: "openai", ID: "o4-mini", DisplayName: "o4-mini",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 1.1, PriceOutput: 4.4, PriceCacheRead: 0.28,
	},
	{
		Provider: "openai", ID: "o3", DisplayName: "o3",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 2, PriceOutput: 8, PriceCacheRead: 0.5,
	},
	{
		Provider: "openai", ID: "o3-mini", DisplayName: "o3-mini",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 1.1, PriceOutput: 4.4, PriceCacheRead: 0.55,
	},
	{
		Provider: "openai", ID: "o1", DisplayName: "o1",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 15, PriceOutput: 60, PriceCacheRead: 7.5,
	},

	// ---- Google / Gemini ----
	{
		Provider: "google", ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro",
		ContextWindow: 1048576, MaxOutput: 65536, Reasoning: true,
		PriceInput: 1.25, PriceOutput: 10, PriceCacheRead: 0.125,
	},
	{
		Provider: "google", ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash",
		ContextWindow: 1048576, MaxOutput: 65536, Reasoning: true,
		PriceInput: 0.3, PriceOutput: 2.5, PriceCacheRead: 0.03,
	},
	{
		Provider: "google", ID: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash-Lite",
		ContextWindow: 1048576, MaxOutput: 65536, Reasoning: true,
		PriceInput: 0.1, PriceOutput: 0.4, PriceCacheRead: 0.01,
	},
	{
		Provider: "google", ID: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash",
		ContextWindow: 1048576, MaxOutput: 8192, Reasoning: false,
		PriceInput: 0.1, PriceOutput: 0.4, PriceCacheRead: 0.025,
	},
	{
		Provider: "google", ID: "gemini-2.0-flash-lite", DisplayName: "Gemini 2.0 Flash-Lite",
		ContextWindow: 1048576, MaxOutput: 8192, Reasoning: false,
		PriceInput: 0.075, PriceOutput: 0.3, PriceCacheRead: 0,
	},

	// ---- OpenRouter ----
	// Seed entry only: the default model returned by defaultModelForProvider
	// so it resolves offline. The full OpenRouter catalog is discovered live
	// (DiscoverOpenRouter) and overlays this on refresh.
	{
		Provider: "openrouter", ID: "anthropic/claude-sonnet-4.5", DisplayName: "Claude Sonnet 4.5 (OpenRouter)",
		ContextWindow: 1000000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
		BaseURL: openrouterDefaultBaseURL,
	},

	// ---- Speculative: Anthropic ----
	{
		Provider: "anthropic", ID: "claude-opus-4-5", DisplayName: "Claude Opus 4.5 (latest)",
		ContextWindow: 200000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6",
		ContextWindow: 1000000, MaxOutput: 64000, Reasoning: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5",
		ContextWindow: 1000000, MaxOutput: 64000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
		Speculative: true,
	},
	{
		Provider: "anthropic", ID: "claude-fable-5", DisplayName: "Claude Fable 5",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 10, PriceOutput: 50, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Speculative: true,
	},

	// ---- Speculative: OpenAI ----
	// Public OpenAI API route. The ChatGPT/Codex subscription route is
	// represented separately below as provider "openai-codex".
	{
		Provider: "openai", ID: "gpt-5.1", DisplayName: "GPT-5.1",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 1.25, PriceOutput: 10, PriceCacheRead: 0.13,
		Speculative: true,
	},
	{
		Provider: "openai", ID: "gpt-5.2", DisplayName: "GPT-5.2",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 1.75, PriceOutput: 14, PriceCacheRead: 0.175,
		Speculative: true,
	},
	{
		Provider: "openai", ID: "gpt-5.4", DisplayName: "GPT-5.4",
		ContextWindow: 272000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 2.5, PriceOutput: 15, PriceCacheRead: 0.25,
		Speculative: true,
	},
	{
		Provider: "openai", ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.75, PriceOutput: 4.5, PriceCacheRead: 0.075,
		Speculative: true,
	},
	{
		Provider: "openai", ID: "gpt-5.5", DisplayName: "GPT-5.5",
		ContextWindow: 272000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 5, PriceOutput: 30, PriceCacheRead: 0.5,
		Speculative: true,
	},

	// ---- OpenAI Codex / ChatGPT subscription backend ----
	// Same model ids as the OpenAI family, but routed through the
	// ChatGPT Codex OAuth backend rather than api.openai.com.
	{
		// Text-only research preview, ChatGPT Pro entitlement; 128k
		// window (not the 272k of its mainline siblings). No CapImageOutput:
		// text-only, so the Responses image_generation tool doesn't apply
		// (every other codex model here was live-verified to support it).
		Provider: "openai-codex", ID: "gpt-5.3-codex-spark", DisplayName: "GPT-5.3 Codex Spark",
		ContextWindow: 128000, MaxOutput: 32000, Reasoning: true,
		PriceInput: 1.75, PriceOutput: 14, PriceCacheRead: 0.175,
	},
	{
		Provider: "openai-codex", ID: "gpt-5.4", DisplayName: "GPT-5.4",
		ContextWindow: 272000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 2.5, PriceOutput: 15, PriceCacheRead: 0.25,
		Caps: map[Capability]bool{CapImageOutput: true},
	},
	{
		Provider: "openai-codex", ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini",
		ContextWindow: 272000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.75, PriceOutput: 4.5, PriceCacheRead: 0.075,
		Caps: map[Capability]bool{CapImageOutput: true},
	},
	{
		Provider: "openai-codex", ID: "gpt-5.5", DisplayName: "GPT-5.5",
		ContextWindow: 272000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 5, PriceOutput: 30, PriceCacheRead: 0.5,
		// Native image output via the Responses image_generation tool —
		// live-verified against the Codex subscription backend (2026-07-17).
		Caps: map[Capability]bool{CapImageOutput: true},
	},

	// GPT-5.6 tiers (Sol/Terra/Luna), GA 2026-07-09 on both the API and
	// the Codex subscription backend. `gpt-5.6` upstream aliases Sol.
	{
		Provider: "openai-codex", ID: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true,
		PriceInput: 5, PriceOutput: 30, PriceCacheRead: 0.5, PriceCacheWrite: 6.25,
		Caps: map[Capability]bool{CapImageOutput: true},
	},
	{
		Provider: "openai-codex", ID: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true,
		PriceInput: 2.5, PriceOutput: 15, PriceCacheRead: 0.25, PriceCacheWrite: 3.125,
		Caps: map[Capability]bool{CapImageOutput: true},
	},
	{
		Provider: "openai-codex", ID: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true,
		PriceInput: 1, PriceOutput: 6, PriceCacheRead: 0.1, PriceCacheWrite: 1.25,
		Caps: map[Capability]bool{CapImageOutput: true},
	},
}

// DefaultModel is used when the user does not specify one.
var DefaultModel = Catalog[0] // claude-sonnet-4-5

// ----- active (merged) catalog -----
//
// Callers should use Active() / FindModel / ModelsForProvider for
// lookups. The catalog they see is merged from four declarative
// layers, lowest to highest precedence:
//
//	builtin — the baked-in Catalog (extended from init()s)
//	live    — /v1/models discovery or its disk cache (SetLiveModels)
//	extra   — individually registered models, e.g. the
//	          openai-compatible endpoint's listing (RegisterExtraModel)
//	user    — $TERVA_HOME/models.json overrides (SetUserOverrides)
//
// Precedence is data, not call ordering: each setter replaces only
// its own layer and the merge recomputes here. A standard live
// refresh landing after compat discovery can no longer wipe the
// compat models, and models.json overrides survive every refresh
// without being re-applied. (The old single-overlay design enforced
// precedence by Load*/Set* call order across two packages and lost
// RegisterExtraModel entries whenever SetLiveModels ran second.)

var (
	activeMu   sync.RWMutex
	layerLive  []Model
	layerExtra []Model
	layerUser  []UserOverride
	// merged is the cached layer merge, recomputed on every layer
	// write (writes are rare; reads are hot). nil means no layer has
	// ever been set and Active() serves the baked-in Catalog.
	merged []Model
	// catalogRev bumps on every remerge so live UIs (the /model picker)
	// can detect when background /v1/models discovery has grown the
	// catalog and re-read it, instead of holding a stale snapshot.
	catalogRev uint64
)

// remergeLocked recomputes the merged catalog. Callers hold activeMu.
func remergeLocked() {
	out := MergeCatalog(layerLive)
	out = upsertModels(out, layerExtra)
	out = applyUserOverrides(out, layerUser)
	merged = out
	catalogRev++
}

// CatalogRevision returns a counter that increments whenever the active
// catalog is recomputed (a layer write — e.g. live discovery completing).
// A long-lived view can poll it to know when to re-read Active().
func CatalogRevision() uint64 {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return catalogRev
}

// upsertModels overlays layer onto base: same provider/id replaces in
// place (wholesale), new entries append in layer order.
func upsertModels(base, layer []Model) []Model {
	if len(layer) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	for i, m := range base {
		index[m.Provider+"/"+m.ID] = i
	}
	for _, m := range layer {
		if i, ok := index[m.Provider+"/"+m.ID]; ok {
			base[i] = m
			continue
		}
		base = append(base, m)
		index[m.Provider+"/"+m.ID] = len(base) - 1
	}
	return base
}

// SetLiveModels replaces the "live" layer. Typically called after a
// successful /v1/models discovery or on load from the on-disk cache.
func SetLiveModels(live []Model) {
	activeMu.Lock()
	defer activeMu.Unlock()
	layerLive = append([]Model(nil), live...)
	remergeLocked()
}

// RegisterExtraModel upserts a single model into the "extra" layer,
// replacing any layer entry with the same provider/id. Used for models
// that are neither in the baked-in catalog nor discovered via the
// standard refresh — currently the openai-compatible endpoint's
// models. Entries persist across SetLiveModels calls.
func RegisterExtraModel(m Model) {
	activeMu.Lock()
	defer activeMu.Unlock()
	replaced := false
	for i, e := range layerExtra {
		if e.Provider == m.Provider && e.ID == m.ID {
			layerExtra[i] = m
			replaced = true
			break
		}
	}
	if !replaced {
		layerExtra = append(layerExtra, m)
	}
	remergeLocked()
}

// ResetCatalogLayers clears every overlay layer (live, extra, user),
// returning Active() to the baked-in Catalog. Intended for tests that
// need a pristine catalog regardless of what earlier tests installed.
func ResetCatalogLayers() {
	activeMu.Lock()
	defer activeMu.Unlock()
	layerLive, layerExtra, layerUser, merged = nil, nil, nil, nil
}

// Active returns the current merged catalog.
//
// When no layer has ever been set it returns the fully-assembled
// static Catalog. Reading Catalog at call time (rather than capturing
// it into a package-level var initializer) is load-bearing: the
// extended catalog in catalog_builtin.go / extra_models.go is appended
// from init() functions, which run AFTER package-level var
// initializers. Snapshotting Catalog at var-init time would freeze the
// picker to the curated seed list and drop every extra provider
// (openrouter, groq, xai, ...). The same applies to remergeLocked —
// it only ever runs from a setter call, well after init.
func Active() []Model {
	activeMu.RLock()
	defer activeMu.RUnlock()
	src := merged
	if src == nil {
		src = Catalog
	}
	out := make([]Model, len(src))
	copy(out, src)
	return out
}

// FindModel returns a Model by id, optionally constrained by provider.
// If provider is empty, the first matching id is returned. Looks up
// against the merged active catalog.
func FindModel(provider, id string) (Model, error) {
	for _, m := range Active() {
		if m.ID == id && (provider == "" || m.Provider == provider) {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q (provider=%q)", id, provider)
}

// ModelsForProvider returns all models for the given provider, from the
// merged active catalog.
func ModelsForProvider(provider string) []Model {
	var out []Model
	for _, m := range Active() {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// ComputeCost returns the USD cost for the given usage on model m.
func ComputeCost(m Model, u Usage) float64 {
	const per = 1_000_000.0
	return float64(u.InputTokens)*m.PriceInput/per +
		float64(u.OutputTokens)*m.PriceOutput/per +
		float64(u.CacheReadTokens)*m.PriceCacheRead/per +
		float64(u.CacheWriteTokens)*m.PriceCacheWrite/per
}
