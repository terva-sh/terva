package provider

import ()

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

	// PriceOutputImage is the output rate for IMAGE tokens, for the
	// image-generating models that bill their output at two different
	// rates depending on what they emitted.
	//
	// Gemini's nano-banana family is the case: gemini-3.1-flash-image
	// bills text and thinking at $3/1M but images at $60/1M, on the same
	// model, in the same response. One rate cannot describe that. Pricing
	// every output token at the text rate understates an image turn 20x;
	// pricing them all at the image rate overstates a text turn by the
	// same factor, and these models hold ordinary conversations between
	// pictures.
	//
	// 0 means "this model has one output rate", which is every other model
	// in the catalog, and ApplyCost then prices output exactly as it
	// always did. So a model that never sets this is untouched by it.
	PriceOutputImage float64

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

	// Synthetic marks a model that exists ONLY because the operator wrote it
	// into models.json. No catalog row, no discovery, nothing underneath it.
	//
	// Source alone cannot answer this. Every entry the loader builds carries
	// Source="user" already, and applyUserOverrides stamps it again on a row it
	// merges onto, so a catalog model whose context window someone nudged and a
	// model someone invented both read "user" by the time a picker sees them.
	// The two need different words ("overridden" against "custom") and, more
	// sharply, different destructive actions: removing the models.json entry
	// restores defaults for the first and deletes the second outright.
	//
	// It is not sticky. The flag says "nothing underneath it AS MERGED", so a
	// model that terva later ships a catalog row for, or that /v1/models
	// discovers, comes back from the next merge as a plain override. That is
	// the wanted behavior: the catalog caught up, and the entry is now a tweak
	// on top of a real row rather than the only thing holding it up.
	Synthetic bool

	// Caps holds explicit capability assertions; an absent key means
	// unknown and resolves through capDefaults via Has. Treat as
	// immutable after construction — Model is copied by value
	// everywhere, so the map is shared between copies; the only writer
	// is the layer merge, which builds fresh maps (mergeCaps). All
	// reads go through Has, never the map directly. See
	// docs/plans/archive/model-capabilities.md.
	Caps map[Capability]bool

	// Temperature is this model's default sampling temperature (0–2), set
	// via models.json. nil leaves it to the global config / provider
	// default. AdaptiveThinking models ignore it (they reject sampling
	// params). Launch resolve order: --temperature flag > per-model > global
	// config. One of the registry-driven scalar params (see ScalarParams).
	Temperature *float32

	// DefaultReasoning is the reasoning level this model uses when the user has
	// set NO global level (--reasoning / config unset). A raw level string
	// (off/minimum/low/medium/high/maximum/max); "" means no default (off), the
	// prior behavior. Ships in the catalog and is overridable per-model via
	// models.json `defaultReasoning`. NORMALIZED at the point of use, so a
	// literal "off" here forces thinking off for the model unless the user sets a
	// global level. Some endpoints (Kimi K3) silently downgrade to an older model
	// when no thinking is sent, so their catalog rows set this to "high".
	DefaultReasoning string

	// DefaultReasoningSet marks a DefaultReasoning the OPERATOR chose in
	// models.json, as opposed to one the catalog shipped. Set by
	// applyUserOverrides; never by the catalog. Same distinction, and for the
	// same reason, as DisplayNameSet below.
	//
	// 🪤 The two are not interchangeable and collapsing them breaks one of the
	// callers. A catalog DefaultReasoning is terva's FALLBACK — the k3 rows
	// carry "high" only so the endpoint stops silently downgrading to K2, and
	// the comment on those rows says in as many words that it applies "unless
	// the user sets a global level". So the catalog value must stay BELOW the
	// global setting. An operator's models.json value is the opposite: it is a
	// deliberate per-model choice, and a global default that overrode it would
	// make the field unreachable for anyone who has ever touched /settings.
	// Hence one field, two precedence slots, told apart by this flag.
	DefaultReasoningSet bool

	// ReasoningEfforts is the set of reasoning_effort values this model
	// actually accepts on the chat-completions wire ("none", "minimal",
	// "low", "medium", "high", "xhigh", "max"). Set per-model in models.json
	// as `reasoningEfforts`.
	//
	// EMPTY MEANS UNLIMITED, and that is the whole contract: a model nobody
	// has described must never lose a rung. Only a model that declares its
	// efforts is held to them, so this can never make an undeclared model
	// worse than it is today.
	//
	// It exists because the enum is model-dependent, not wire-dependent.
	// OpenAI's own guide says "some models support only a subset of these
	// values": gpt-5.5 takes none/low/medium/high/xhigh and rejects minimal
	// and max, while a local qwen server takes low/medium/xhigh and rejects
	// high outright. One hardcoded ladder cannot be right for both, and being
	// wrong costs an HTTP 400 on every turn.
	//
	// Declaring "none" is also the ONLY way terva can honour "off" on a
	// server whose default is to think: off omits the field, and a server
	// that defaults to thinking then thinks at full effort. See
	// clampEffortToDeclared.
	ReasoningEfforts []string

	// DisplayNameSet marks a DisplayName the operator chose in models.json
	// (`name`), as opposed to one the catalog or live discovery supplied.
	// The distinction is what lets a surface that shows the raw id — the
	// status bar, the picker's id column — swap in the operator's name
	// WITHOUT also swapping in catalog names, which run longer than the ids
	// they'd replace ("Claude Sonnet 4.5 (latest)" vs claude-sonnet-4-5).
	// Set by applyUserOverrides; never by the catalog or live layers.
	DisplayNameSet bool
}

// Label is the model's name for a surface that would otherwise print the
// raw id: the operator's models.json `name` when they set one, the id
// otherwise. Catalog display names deliberately do NOT win here — see
// DisplayNameSet. Surfaces that show name and id side by side (the picker's
// second column, `terva models`) want DisplayName directly instead.
func (m Model) Label() string {
	if m.DisplayNameSet && m.DisplayName != "" {
		return m.DisplayName
	}
	return m.ID
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

// knownCapabilities lists every capability terva understands, for
// models.json validation warnings.
func knownCapabilities() []Capability {
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

// catalog is the hardcoded, read-only list of supported models.
// Prices are USD per 1M tokens. The list is curated to what terva's
// clients (Anthropic Messages + OpenAI Chat Completions) can actually
// talk to; models that are only reachable through the OpenAI Responses
// API (o1-pro, o3-pro, gpt-5-pro) are omitted.
var catalog = []Model{
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

	// ---- Anthropic / Claude 5.x ----
	// Live on the public API since 2026-09-01, so this row is NOT
	// speculative: swarm tier resolution skips speculative rows, and a
	// released model must stay reachable there.
	//
	// Thinking is adaptive and always on. The model rejects an explicit
	// thinking budget and non-default sampling params, so AdaptiveThinking
	// must stay set or buildRequest sends a shape the API 400s on.
	//
	// PriceCacheRead is the family exception: 0.025x base input rather than
	// the 0.1x every other Claude model charges. Do not "correct" it to 1.
	{
		Provider: "anthropic", ID: "claude-fable-5-1", DisplayName: "Claude Fable 5.1",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 10, PriceOutput: 50, PriceCacheRead: 0.25, PriceCacheWrite: 12.5,
	},
	// Opus 5.5 succeeds Opus 5 in the same tier and undercuts it: $4/$20
	// against Opus 5's $5/$25. Everything else about the shape is the same —
	// 1M context, 128K output, the same tokenizer.
	//
	// Not Speculative, for the reason above, and it is the anthropic
	// provider's defaultModel (provider_registry.go), so the row has to
	// resolve offline.
	//
	// AdaptiveThinking is not optional here. Opus 5.5 went further than the
	// rest of the family: thinking cannot be turned off at all, and BOTH
	// thinking:{type:"disabled"} and an explicit budget are a 400 at every
	// effort level. Effort is the only depth control left.
	//
	// PriceCacheRead is the second Claude row to break the 0.1x rule, and it
	// breaks it differently from Fable 5.1's 0.025x: Anthropic publishes
	// $0.20 against the $4 base, which is 0.05x. The pricing guard carries
	// the exception, so a "correction" to 0.4 fails the suite rather than
	// silently doubling every cached-token cost.
	//
	// Both cache rates are Anthropic's PUBLISHED figures, confirmed against
	// the rate card after this row first landed carrying a derived write.
	// The 5-minute write is $5, which is also 1.25x base — the derivation
	// and the published number agree, and it is recorded here so nobody
	// re-derives it and wonders which one to trust. The 1-hour write is $8
	// (2x base) and is deliberately unmodelled: PriceCacheWrite holds the
	// 5-minute rate, the only one terva can incur, because buildRequest
	// never sends ttl:"1h".
	{
		Provider: "anthropic", ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5",
		ContextWindow: 1000000, MaxOutput: 128000, Reasoning: true, AdaptiveThinking: true,
		PriceInput: 4, PriceOutput: 20, PriceCacheRead: 0.2, PriceCacheWrite: 5,
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
	// docs/plans/archive/model-capabilities.md).
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
	// the Anthropic client appends /v1/messages itself). The subscription
	// (device-code OAuth) and KIMI_API_KEY both resolve to this one `kimi`
	// provider, so these rows serve both. The k3 rows' DefaultReasoning:
	// "high" keeps them sending `thinking`: the endpoint silently
	// downgrades to Kimi 2.6 when a request arrives with no thinking, so a
	// model default (not a hardcoded branch) keeps K3 answering as K3
	// unless the user sets a global level. k3-256k is the same model
	// behind a 256k window instead of 1M.
	//
	// PRICES ARE THE MOONSHOT PLATFORM'S LIST RATES, not what a Kimi Code
	// subscriber is billed — which is nothing per token. That is deliberate
	// and it is what every other subscription provider here does: the
	// openai-codex rows carry OpenAI's list prices, and the status bar
	// renders the result as "$0.529 ~$0.71/hr (sub)" — an estimate of what
	// the session WOULD have cost, with "(sub)" saying no real money moved.
	//
	// These rows carried explicit zeros until 2026-07-30, on the premise that
	// a subscription has no price. The result was "$0.000 (sub)" on every
	// Kimi session — the badge with nothing to badge, and the one provider
	// where the readout said nothing. Prices mirror the first-party
	// moonshotai rows for the same models (catalog_builtin.go), which is the
	// only published rate either model has.
	//
	// PriceCacheWrite is deliberately unset, as on every moonshotai row:
	// Moonshot publishes a cache-hit rate and no separate cache-WRITE rate,
	// and no observed kimi response has ever reported cache_creation tokens.
	// If one ever does, this is the field that needs a source — leaving it
	// zero would price cache writes as free and overstate what caching saved.
	{
		Provider: "kimi", ID: "k3", DisplayName: "Kimi K3",
		ContextWindow: 1000000, MaxOutput: 32768, Reasoning: true,
		DefaultReasoning: "high",
		PriceInput:       3, PriceOutput: 15, PriceCacheRead: 0.3,
		BaseURL: "https://api.kimi.com/coding",
	},
	{
		Provider: "kimi", ID: "k3-256k", DisplayName: "Kimi K3 256k",
		ContextWindow: 262144, MaxOutput: 32768, Reasoning: true,
		DefaultReasoning: "high",
		PriceInput:       3, PriceOutput: 15, PriceCacheRead: 0.3,
		BaseURL: "https://api.kimi.com/coding",
	},
	{
		// The K2-generation rows. kimi-for-coding is the plan's legacy id and
		// has no published rate of its own; it is priced as K2 Thinking, its
		// generation-mate on the same endpoint and window. A proxy, and said
		// so — the same treatment moonshotai/k3-256k already gets.
		Provider: "kimi", ID: "kimi-for-coding", DisplayName: "Kimi For Coding",
		ContextWindow: 262144, MaxOutput: 32768, Reasoning: true,
		PriceInput: 0.6, PriceOutput: 2.5, PriceCacheRead: 0.15,
		BaseURL: "https://api.kimi.com/coding",
	},
	{
		// Curated here rather than in the two places it used to sit. It was
		// listed in BOTH catalog_builtin.go and extra_models.go, each with a
		// comment asserting it was not in the other; which of the two won was
		// down to the alphabetical order of their init functions. Its four
		// siblings live here, so it does too.
		Provider: "kimi", ID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking",
		ContextWindow: 262144, MaxOutput: 32768, Reasoning: true,
		PriceInput: 0.6, PriceOutput: 2.5, PriceCacheRead: 0.15,
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
	//
	// Deliberately empty. This block held the 2.5 and 2.0 families, and every
	// one of them is now RETIRED on the Generative Language API — probed
	// 2026-08-14 with a live key, all five answered 404: the 2.5 rows with "no
	// longer available to new users", the 2.0 rows with "no longer available".
	// A curated row outranks the built-in catalog on an (provider, id) match,
	// so these five did not merely sit unused: they were the FIRST thing a
	// model lookup found, and gemini-2.5-pro was the provider default, which
	// is why a fresh `terva --provider google` 404'd before it sent a token.
	//
	// The live google rows live in catalog_builtin.go. Nothing needs a seed row
	// here; this comment exists so the next person does not "restore" them.

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
		Provider: "anthropic", ID: "claude-opus-5", DisplayName: "Claude Opus 5",
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
		// Cache rates are Fable's, not Opus's: 0.1x base input for a read and
		// 1.25x for a 5-minute write. This row carried the Opus 5 pair
		// (0.5 / 6.25) and halved every cached-token cost it reported.
		PriceInput: 10, PriceOutput: 50, PriceCacheRead: 1, PriceCacheWrite: 12.5,
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
	{
		// OpenAI announced a staged rollout on 2026-09-03. Catalog presence
		// lets entitled accounts select Astra without claiming universal access.
		// CapImageOutput stays unset until this route is live-verified.
		Provider: "openai-codex", ID: "gpt-6-astra", DisplayName: "GPT-6 Astra",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true, ReasoningEfforts: gpt6AstraEfforts,
		PriceInput: 10, PriceOutput: 50, PriceCacheRead: 1, PriceCacheWrite: 12.5,
	},
	{
		// OpenAI launched Sol and Luna on 2026-09-22 for Plus, Pro, Business,
		// Enterprise, and Edu. Same windows and surcharge point as Astra.
		// CapImageOutput stays unset until this route is live-verified.
		Provider: "openai-codex", ID: "gpt-6-sol", DisplayName: "GPT-6 Sol",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true, ReasoningEfforts: gpt6Efforts,
		PriceInput: 2, PriceOutput: 10, PriceCacheRead: 0.2, PriceCacheWrite: 2.5,
	},
	{
		Provider: "openai-codex", ID: "gpt-6-luna", DisplayName: "GPT-6 Luna",
		ContextWindow: 1050000, DesiredContextWindow: 272000, ContextSurchargeAt: 272000,
		MaxOutput: 128000, Reasoning: true, ReasoningEfforts: gpt6Efforts,
		PriceInput: 0.1, PriceOutput: 0.5, PriceCacheRead: 0.01, PriceCacheWrite: 0.125,
	},
}

// The GPT-6 rows declare OpenAI's published reasoning_effort sets. Only the
// chat-completions mapper reads ReasoningEfforts; the Responses routes these
// rows belong to use openAICodexReasoningEffort and ignore it. The lists are
// here for openai-compatible gateways serving the same ids. Discovery copies
// them from these rows (openAICompatCaps), which does two things for such a
// gateway:
//
//   - Off sends reasoning_effort "none" rather than omitting the field. On
//     OpenAI's Chat Completions an omitted effort means "medium", and Sol and
//     Luna refuse function calls at any effort but "none", so omitting it
//     broke tool calling on a gateway that passes Chat Completions through.
//     Any thinking level above off still cannot call tools there; only a
//     gateway that converts to the Responses API can, at any effort.
//   - "maximum" and "max" reach xhigh and max, where the undeclared mapper
//     clamps both to "high" for fear of an unknown server.
//
// Astra's page lists no "none", so off still omits the field for it.
// Every row with a given id must declare the same set, or discovery drops
// the list rather than pick one (see sameEffortSet). The codex and Responses
// rows therefore share these variables.
var (
	gpt6Efforts      = []string{"none", "low", "medium", "high", "xhigh", "max"}
	gpt6AstraEfforts = []string{"low", "medium", "high", "xhigh", "max"}
)

// DefaultModel is used when the user does not specify one.
//
// Unstable: terva's own convention carries no promise before 1.0.
var DefaultModel Model = catalog[0] // claude-sonnet-4-5

// ----- active (merged) catalog -----
//
// A Registry (registry.go) merges four declarative layers, lowest to
// highest precedence, and its host reads it through Active, FindModel
// and ModelsForProvider:
//
//	builtin — the baked-in catalog (extended from init()s)
//	live    — /v1/models discovery or its disk cache (SetLiveModels)
//	extra   — individually registered models, e.g. the
//	          openai-compatible endpoint's listing (RegisterExtraModel)
//	user    — $TERVA_HOME/models.json overrides (SetUserOverrides)
//
// Precedence is data, not call ordering: each setter replaces only
// its own layer and the merge recomputes. A standard live
// refresh landing after compat discovery can no longer wipe the
// compat models, and models.json overrides survive every refresh
// without being re-applied. (The old single-overlay design enforced
// precedence by Load*/Set* call order across two packages and lost
// RegisterExtraModel entries whenever SetLiveModels ran second.)

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

// The package holds no Registry: the host owns one and passes it in
// (docs/plans/model-catalog.md).

// computeCost returns the USD cost for the given usage on model m.
//
// Output is billed at one rate unless the model sets PriceOutputImage, in
// which case the image tokens inside OutputTokens are split out and billed
// at their own rate. See Model.PriceOutputImage and Usage.ImageOutputTokens:
// the image models bill the two 10-20x apart, and both directions of getting
// it wrong are real money.
func computeCost(m Model, u Usage) float64 {
	const per = 1_000_000.0
	return float64(u.InputTokens)*m.PriceInput/per +
		outputCost(m, u) +
		float64(u.CacheReadTokens)*m.PriceCacheRead/per +
		float64(u.CacheWriteTokens)*m.PriceCacheWrite/per
}

// outputCost prices OutputTokens, splitting image tokens onto their own rate
// when the model has one.
//
// ImageOutputTokens is a subset of OutputTokens, so the text-rate base is the
// remainder. Anything else would bill the image tokens twice.
func outputCost(m Model, u Usage) float64 {
	const per = 1_000_000.0
	if m.PriceOutputImage == 0 {
		// Every model with a single output rate, which is all of them
		// but the image generators. Byte-identical to the old formula.
		return float64(u.OutputTokens) * m.PriceOutput / per
	}
	image := u.ImageOutputTokens
	if image > u.OutputTokens {
		// A provider that reports a bigger subset than its own total is
		// describing something this code does not model. Bill the whole
		// output at the image rate rather than let the remainder go
		// negative and refund the difference.
		image = u.OutputTokens
	}
	text := u.OutputTokens - image
	return float64(text)*m.PriceOutput/per + float64(image)*m.PriceOutputImage/per
}

// cacheSavings returns what the prompt cache was worth on this response:
// the prompt billed at full input price, minus the prompt as actually
// billed. Negative when cache writes outweigh the reads they enabled.
//
// A model with no cache pricing (PriceCacheRead and PriceCacheWrite both
// zero) returns 0 rather than the full prompt price. Free reads would
// otherwise report the whole prompt as "saved" on every local ollama turn,
// where the honest answer is that nothing was billed and nothing was saved.
func cacheSavings(m Model, u Usage) float64 {
	if m.PriceCacheRead == 0 && m.PriceCacheWrite == 0 {
		return 0
	}
	const per = 1_000_000.0
	uncached := float64(u.PromptTokens()) * m.PriceInput / per
	billed := float64(u.InputTokens)*m.PriceInput/per +
		float64(u.CacheReadTokens)*m.PriceCacheRead/per +
		float64(u.CacheWriteTokens)*m.PriceCacheWrite/per
	return uncached - billed
}

// ApplyCost stamps both money fields on a usage record from the model that
// produced it. The single place a decoder prices a response.
//
// It exists so the two stay together. CacheSavedUSD can only be computed
// here — the model is in scope, and by the time the usage row is read back
// the price sheet that applied to it is gone (a session switches models;
// the row records no model). A decoder that set CostUSD and forgot the
// savings would silently report a session as having saved nothing, so
// TestEveryProviderPricesThroughApplyCost holds every decoder to this door
// — and finds a new one by what it assigns, not by a list someone has to
// remember to extend.
func ApplyCost(m Model, u *Usage) {
	u.CostUSD = computeCost(m, *u)
	u.CacheSavedUSD = cacheSavings(m, *u)
}
