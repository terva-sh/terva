// Package provider defines the LLM client abstraction used by terva.
//
// It supports several providers behind one Client interface — Anthropic
// (Messages API), OpenAI (Chat Completions and the Codex/Responses
// API), Google Gemini, Amazon Bedrock, Kimi, Ollama, and any
// OpenAI-compatible endpoint. Everything above this package operates on
// the types declared here and does not know about HTTP or SSE.
package provider

import (
	"context"
	"encoding/json"
	"time"
)

// Role is the speaker of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Content is a block inside a Message. One of TextBlock, ImageBlock,
// ToolCallBlock, or ToolResultBlock.
type Content interface {
	isContent()
}

// TextBlock is plain text content.
type TextBlock struct {
	Text string `json:"text"`
}

func (TextBlock) isContent() {}

// ImageBlock is an inline image (PNG/JPEG/GIF/WebP).
type ImageBlock struct {
	MimeType string `json:"mime_type"`
	Data     []byte `json:"data"` // raw bytes; encoded to base64 on the wire
	// ID is the provider-issued generation id for an image the assistant
	// produced itself (OpenAI Responses "ig_…" image_generation_call id),
	// carried so a later turn can replay the image to edit it. Empty for
	// image *inputs* (the read tool, pasted images), which no provider
	// treats as editable and which never round-trip as a generation call.
	ID string `json:"id,omitempty"`
}

func (ImageBlock) isContent() {}

// ToolCallBlock is an assistant-issued call to a tool.
type ToolCallBlock struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is ALWAYS valid JSON — see FinalizeToolArguments, which every
	// provider routes its streamed buffer through. Callers may marshal a block
	// without checking; an invalid value here would break the session writer,
	// the request builders, and the ctrlproto wire alike.
	Arguments json.RawMessage `json:"arguments"`
	// RawArguments holds the model's original argument text when it could not
	// be parsed or repaired, and is empty otherwise. Arguments is "{}" in that
	// case, so the block stays marshalable while the evidence of what the model
	// tried to send survives for the transcript and for the error the model is
	// given back.
	RawArguments string `json:"raw_arguments,omitempty"`
}

func (ToolCallBlock) isContent() {}

// ToolResultBlock is the result of a tool execution, attached to a
// Message with Role == RoleTool.
type ToolResultBlock struct {
	CallID  string    `json:"call_id"`
	Content []Content `json:"content"`
	IsError bool      `json:"is_error"`
}

func (ToolResultBlock) isContent() {}

// ReasoningBlock carries the assistant's chain-of-thought metadata so
// providers that require it on follow-up requests (OpenAI Codex with
// thinking enabled) can replay the same payload they emitted earlier.
// Summary is the human-readable reasoning summary (may be empty); the
// encrypted blob is opaque to terva. ID is the provider-issued reasoning
// item id.
type ReasoningBlock struct {
	ID        string `json:"reasoning_id,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Encrypted string `json:"encrypted_content,omitempty"`
}

func (ReasoningBlock) isContent() {}

// CompactionBlock carries a server-side compaction summary: an opaque blob
// in which the backend has encoded the conversation it compacted away, to be
// replayed on later requests in place of the turns it replaces.
//
// It exists because the OpenAI Responses backend compacts through its own
// endpoint (POST <base>/compact) rather than leaving it to the client, and
// answers with items to send back as input — the last of which is this. Terva
// has always summarized client-side into a synthetic user message instead,
// which is a shape the backend never produced and, on the codex backend, is
// where every measured prompt-cache collapse begins. See
// docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md §9.1.
//
// 🪤 The wire type is `compaction_summary`, NOT `compaction` — the published
// write-ups say otherwise and a live probe says this. Encrypted is opaque:
// terva must round-trip it byte for byte and can never inspect or rebuild it,
// so any surface that drops this block silently discards the only record of
// what the compaction removed.
type CompactionBlock struct {
	ID        string `json:"compaction_id,omitempty"`
	Encrypted string `json:"encrypted_content,omitempty"`
	// Provider names who issued the blob. Only that provider can decrypt it,
	// so this is what lets a later dispatch tell "replayable" from "so much
	// opaque text" BEFORE handing the transcript to a serializer.
	//
	// Provider, not provider+model: measured 2026-08-04, a blob compacted on
	// gpt-5.6-terra replays on sol and on gpt-5.5, all three recalling content
	// that existed only inside it. A /model switch within one provider is
	// safe; a provider switch is not.
	Provider string `json:"provider,omitempty"`
}

func (CompactionBlock) isContent() {}

// ForeignCompactions reports the indices of messages carrying a compaction
// blob that provider cannot replay.
//
// This lives above the serializers on purpose. Every provider's content switch
// is a type switch with no default arm, so an unrecognized block is dropped
// silently — and for a compaction that is not degradation but amnesia: the blob
// is the only encoding of the assistant turns it replaced, so the model receives
// a conversation that reads continuous and is missing half its history, with
// nothing raised anywhere. Leaving this to each provider to remember is how it
// stayed invisible; asking once, here, is what makes it a decision.
//
// A blob with no recorded provider is treated as foreign to everyone: it
// predates provenance, and guessing that it belongs to whoever is asking is the
// answer that loses data.
func ForeignCompactions(msgs []Message, providerName string) []int {
	var out []int
	for i, m := range msgs {
		for _, c := range m.Content {
			cb, ok := c.(CompactionBlock)
			if !ok {
				continue
			}
			if cb.Provider == "" || cb.Provider != providerName {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// RepairOrphanedToolResults removes tool_result content blocks (and
// entire messages that become empty) when the matching tool_use ID
// does not appear anywhere in the given messages. Resume tails,
// compaction repair, and provider request builders all need this so
// the upstream API never sees a tool_call_id with no corresponding
// assistant tool_call earlier in the same request.
func RepairOrphanedToolResults(msgs []Message) []Message {
	useIDs := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if tc, ok := c.(ToolCallBlock); ok {
				useIDs[tc.ID] = true
			}
		}
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		var filtered []Content
		for _, c := range m.Content {
			if tr, ok := c.(ToolResultBlock); ok {
				if !useIDs[tr.CallID] {
					continue
				}
			}
			filtered = append(filtered, c)
		}
		if len(filtered) > 0 {
			copy := m
			copy.Content = filtered
			out = append(out, copy)
		}
	}
	return out
}

// EnsureLeadingUserTurn prepends a minimal, request-scoped user turn when the
// first message is an assistant turn. A character card seeds its opening
// greeting as an assistant message[0]; the Anthropic, Bedrock Converse, and
// Gemini APIs all reject a conversation that does not begin with a user turn,
// and the OpenAI-family builders apply it too as a safety net for strict
// OpenAI-compatible backends (Moonshot/Kimi, local alternation-enforcing
// templates) that share the wire format. Prepending keeps the greeting in the
// transcript while the request stays well-formed. It returns a new slice and
// never mutates the input, so the added turn is request-scoped and is never
// cached as part of the history prefix. Dormant for normal conversations,
// which always open with a user turn.
//
// The placeholder is a bracketed stage cue, not an utterance: a greeting like
// "Hello." would recast a character-initiated opening (an ambush, a monologue)
// as a reply to the user, distorting the scene on every request for the whole
// session. It must carry visible text — these APIs also reject empty or
// whitespace-only blocks.
func EnsureLeadingUserTurn(msgs []Message) []Message {
	if len(msgs) == 0 || msgs[0].Role != RoleAssistant {
		return msgs
	}
	return append([]Message{{
		Role:    RoleUser,
		Content: []Content{TextBlock{Text: "[Begin.]"}},
	}}, msgs...)
}

// MergeAdjacentSameRole coalesces consecutive messages that share a role into one
// by concatenating their content — a request-scoped repair for the strict
// alternation the Anthropic, Bedrock, and Gemini APIs enforce (and that some
// OpenAI-compatible backends, e.g. Moonshot/Kimi and local templates, do too).
//
// Transcript revision can leave two same-role turns adjacent: deleting the
// assistant reply between two user messages, editing across a boundary, or a
// compaction summary landing next to a same-role turn. Those APIs reject the
// resulting sequence with a 400. Merging restores alternation while keeping every
// turn's content. It runs AFTER tool-pair repair, so tool_use/tool_result
// structure is already settled and merging same-role turns cannot split a pair.
//
// It returns a new slice and copies any message it merges into, so the input and
// its messages are never mutated — the merge is request-scoped and never cached
// as part of the history prefix. Dormant for well-formed transcripts, which
// already alternate (a step's parallel tool results are one RoleTool message, so
// normal turns never trip it).
func MergeAdjacentSameRole(msgs []Message) []Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			prev := out[n-1] // copy so the source message's slice is never aliased
			prev.Content = append(append([]Content(nil), prev.Content...), m.Content...)
			out[n-1] = prev
			continue
		}
		out = append(out, m)
	}
	return out
}

// Message is a single turn in the conversation.
type Message struct {
	Role    Role              `json:"role"`
	Content []Content         `json:"content"`
	Time    time.Time         `json:"time"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// Tool is a tool definition advertised to the LLM.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema for arguments
}

// Usage aggregates token counts and cost for a turn.
//
// InputTokens is the UNCACHED remainder of the prompt, never the whole
// prompt. Anthropic reports it that way natively; the OpenAI, Codex and
// Gemini decoders subtract their cached count to match. Every consumer
// depends on that normalization — the context gauge, the compaction
// thresholds and CacheHitRate all read the prompt as
// input+cache_read+cache_write, which double-counts the moment a decoder
// stops subtracting.
type Usage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`

	// CacheSavedUSD is what the prompt cache saved on this usage: the
	// counterfactual cost of the same prompt tokens at full input price,
	// minus what was actually billed for them. Output is untouched by
	// caching and plays no part.
	//
	// Stamped per response, at the prices of the model that answered it
	// (ApplyCost), because it cannot be recovered later: a session that
	// switches models mid-way has no single price sheet, and the usage row
	// records no model. Summed by Add, so a session total stays exact
	// across switches.
	//
	// Signed on purpose. Writing a cache costs MORE than not caching
	// (Anthropic bills creation at 1.25x input), so a session that keeps
	// invalidating its prefix and re-writing it genuinely runs negative —
	// which is the single most useful thing this number can say.
	CacheSavedUSD float64 `json:"cache_saved_usd,omitempty"`

	// ReasoningTokens is how much of OutputTokens the model spent thinking.
	//
	// A SUBSET of OutputTokens, not a fourth disjoint bucket. The prompt
	// fields above are disjoint because each is billed at its own rate;
	// reasoning is billed at the output rate and is already inside
	// OutputTokens, so subtracting it here would silently change every bill.
	// ComputeCost does not read this field, and a guard pins that.
	//
	// Informational on purpose: it is the one part of a reasoning model's
	// spend that is otherwise invisible, and on this codebase the question
	// "how much of that output was thinking?" comes up whenever a session
	// costs more than its transcript explains.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	// ReasoningTokensKnown separates "the model reported 0 reasoning tokens"
	// from "this provider does not break reasoning out at all".
	//
	// Anthropic is the second case: thinking rides inside output_tokens with
	// no separate count, so a 0 there is an absence of information rather
	// than a measurement. Without this flag a session total would quietly
	// understate, which is the failure mode a cost breakdown exists to avoid.
	ReasoningTokensKnown bool `json:"reasoning_tokens_known,omitempty"`
}

// Add returns u plus v.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		InputTokens:          u.InputTokens + v.InputTokens,
		OutputTokens:         u.OutputTokens + v.OutputTokens,
		CacheReadTokens:      u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens:     u.CacheWriteTokens + v.CacheWriteTokens,
		CostUSD:              u.CostUSD + v.CostUSD,
		CacheSavedUSD:        u.CacheSavedUSD + v.CacheSavedUSD,
		ReasoningTokens:      u.ReasoningTokens + v.ReasoningTokens,
		ReasoningTokensKnown: mergeReasoningKnown(u, v),
	}
}

// mergeReasoningKnown folds the known-ness of two usage reports.
//
// A total is only fully known when every component reported a reasoning
// count — one Anthropic turn in a session makes the session's reasoning
// figure a floor rather than a total, and it must say so.
//
// The zero Usage is the exception, and it is not a special case for its own
// sake: CostTracker accumulates with `total = total.Add(turn)` starting from
// Usage{}, so a plain AND would mark every total unknown no matter what the
// providers reported. An empty accumulator has not reported anything, so it
// carries no opinion.
func mergeReasoningKnown(u, v Usage) bool {
	if u.isZero() {
		return v.ReasoningTokensKnown
	}
	if v.isZero() {
		return u.ReasoningTokensKnown
	}
	return u.ReasoningTokensKnown && v.ReasoningTokensKnown
}

// isZero reports whether u carries no measurement at all — the state of a
// freshly constructed accumulator, as distinct from a turn that genuinely
// billed nothing but was still reported.
func (u Usage) isZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 &&
		u.ReasoningTokens == 0 && !u.ReasoningTokensKnown &&
		u.CostUSD == 0 && u.CacheSavedUSD == 0
}

// PromptTokens is everything the model read: the uncached remainder plus
// whatever came from, or went into, the cache. The denominator of every
// cache ratio, and the same sum the context gauge shows.
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// CacheHitRate is the share of the prompt served from cache, in [0,1].
//
// Zero prompt tokens returns (0, false) rather than 0: "no requests yet"
// and "every request missed" are different states, and a panel that draws
// an empty bar for both says the cache is failing when nothing has been
// asked of it. Every caller must decide which it is showing.
func (u Usage) CacheHitRate() (rate float64, ok bool) {
	prompt := u.PromptTokens()
	if prompt <= 0 {
		return 0, false
	}
	return float64(u.CacheReadTokens) / float64(prompt), true
}

// StopReason describes why a turn ended.
type StopReason string

const (
	StopEnd     StopReason = "end"
	StopLength  StopReason = "length"
	StopToolUse StopReason = "tool_use"
	StopError   StopReason = "error"
	StopAborted StopReason = "aborted"
)

// Event is one item from a provider stream.
type Event interface {
	isEvent()
}

type EventStart struct {
	Model    string
	Provider string
}

func (EventStart) isEvent() {}

type EventTextDelta struct {
	Delta string
}

func (EventTextDelta) isEvent() {}

type EventToolStart struct {
	ID   string
	Name string
}

func (EventToolStart) isEvent() {}

type EventToolArgs struct {
	ID    string
	Delta string // partial JSON
}

func (EventToolArgs) isEvent() {}

type EventToolEnd struct {
	ID string
}

func (EventToolEnd) isEvent() {}

type EventUsage struct {
	Usage Usage
}

func (EventUsage) isEvent() {}

// EventDone is always the final event on a stream.
type EventDone struct {
	Stop    StopReason
	Err     error
	Message Message // fully assembled assistant message
}

func (EventDone) isEvent() {}

// Request is a single LLM call.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature *float32
	// Reasoning is "", "minimum", "low", "medium", "high", "maximum", or
	// "max". Empty disables reasoning. Budget-based providers map these to
	// roughly 1k/2k/8k/16k/32k thinking tokens; effort-based providers map
	// them onto their closest supported reasoning_effort values. "max" is
	// sent natively only to models that support it (GPT-5.6, adaptive
	// Claude) and clamped to the "maximum" effort elsewhere.
	Reasoning string

	// ReasoningSet reports the user explicitly chose the global reasoning level
	// (including "off"), so it wins over a model's DefaultReasoning. False means
	// unset — fall back to the model default.
	ReasoningSet bool

	// ReasoningSummary asks the provider to emit a human-readable summary of
	// its reasoning alongside the opaque payload, so an autonomous run's
	// session record shows WHY it acted and not only what it did. Values are
	// provider-defined; the OpenAI Responses/codex backend accepts "auto",
	// "concise", and "detailed". Empty (the default) requests no summary and
	// leaves the request byte-identical to one built without this field.
	// Only the codex client acts on it today — a summary is only ever emitted
	// when asked for, so this is inert everywhere else.
	ReasoningSummary string

	// EphemeralContext is host-assembled, host-wrapped text injected into
	// the model's context for THIS request only — never part of Messages
	// and never persisted to the transcript. Providers append it as a
	// trailing message AFTER the cache breakpoint so the cached prefix
	// (system + tools + history) still hits and only this block is
	// re-processed. Used for standing context that changes between turns
	// (e.g. an extension's live task card). Empty means inject nothing.
	EphemeralContext string

	// PromptCacheKey is a stable per-conversation identifier forwarded to
	// providers whose prefix cache uses it for routing (OpenAI's
	// prompt_cache_key, on both Responses and Chat Completions). Without
	// it, concurrent conversations on one account — a coordinator plus its
	// swarm children — hash into overlapping cache shards and evict each
	// other's prefixes. The agent loop sets it from the session's meta
	// UUID (globally unique where the file basename is not — every swarm
	// child's transcript is named session.json); empty sends nothing.
	// Only clients known to accept the field forward it
	// (OpenAI-compatible backends may reject unknown parameters).
	PromptCacheKey string

	// ImageOutput, when non-nil, enables native (in-protocol) image output
	// for this request: the model may draw images inline in its own turn via
	// the provider's built-in image tool (OpenAI Responses image_generation),
	// which arrive as ImageBlocks in the assistant Message. Nil means the
	// tool is not offered. Only clients that implement it act on this; the
	// rest ignore the field. The host gates this (opt-in config + the model's
	// CapImageOutput + not plan mode) before setting it.
	ImageOutput *ImageOutputConfig
}

// ImageOutputConfig configures native image output (see Request.ImageOutput).
type ImageOutputConfig struct {
	// Size and Quality configure the built-in image tool, e.g.
	// "1024x1024"/"1024x1536"/"1536x1024"/"auto" and
	// "low"/"medium"/"high"/"auto". Empty lets the provider default.
	Size    string
	Quality string
	// EditHistory bounds how many of the most recent assistant-generated
	// images are replayed to the model (with their bytes) so it can edit
	// them. Each replayed image re-uploads as image-input tokens every turn,
	// so this trades cost/latency for how far back editing reaches. 0 means
	// generation only (no image replayed); 1 (the default) keeps only the
	// most recent image editable.
	EditHistory int
}

// Client is an LLM streaming client.
type Client interface {
	// Name returns "anthropic" or "openai".
	Name() string
	// Stream starts a request. The returned channel delivers events
	// and is closed after EventDone. Errors during request setup are
	// returned directly; runtime errors arrive as EventDone{Err: ...}.
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// unwrapper is implemented by clients that wrap another Client (e.g.
// renamedClient, pollingUsageClient). Capability probes use it to see
// through wrappers instead of type-asserting directly on the outermost
// client, which silently fails whenever a wrapper sits in front.
type unwrapper interface {
	Unwrap() Client
}

// ClientCapabilities is the explicit, compiler-checked set of
// behaviors a Client can opt into. A new client capability is a field
// here, set in the concrete client's Capabilities() method — never a
// one-off optional interface (`interface{ Foo() bool }`) probed by a
// bespoke chain-walking helper. That older pattern silently returned
// the zero value whenever a wrapper sat in front of the concrete
// client (the MirrorsToolImages bug class): openai-responses and google-vertex ship
// wrapped in a renamedClient (and deepseek in a pollingUsageClient),
// so an inline assertion on the outer client missed the capability
// entirely.
type ClientCapabilities struct {
	// MirrorsToolImages is true for wire formats that can't carry
	// images inside a tool result (OpenAI chat-completions; the
	// Responses/codex function_call_output). The agent loop mirrors
	// such images into a following synthetic user message. This is a
	// WIRE-FORMAT fact; whether the current model can see images at
	// all is the separate per-model image-input capability the loop
	// checks alongside it.
	MirrorsToolImages bool

	// ContinuesAssistantPrefill is true for wire formats that treat a
	// TRAILING assistant message as a prefill to extend — the model
	// continues that message rather than starting a fresh turn. Anthropic
	// (Claude Messages) does; OpenAI treats it as history, Codex marks it
	// completed, and Gemini enforces strict alternation. The Stage
	// "continue" interaction (turn.continue) is gated on this: only a
	// prefill-continuing provider can extend the last response in place.
	ContinuesAssistantPrefill bool
}

// capabilityProvider is implemented by concrete clients that declare
// capabilities. Wrappers must NOT implement it — they expose Unwrap()
// and ClientCaps walks down to the concrete client.
type capabilityProvider interface {
	Capabilities() ClientCapabilities
}

// clientAs walks the unwrap chain (renamedClient, pollingUsageClient, …)
// and returns the first client in it that implements T, plus ok. The
// wrapper walk lives here, once: every capability/reporter probe goes
// through it, so none can reintroduce the silent-zero wrapper gap (the
// MirrorsToolImages bug) by hand-rolling a type assertion on the outer
// client. c must be an interface value (Client), so the c.(T) assertion
// is legal for any T.
func clientAs[T any](c Client) (T, bool) {
	for c != nil {
		if v, ok := c.(T); ok {
			return v, true
		}
		u, ok := c.(unwrapper)
		if !ok {
			break
		}
		c = u.Unwrap()
	}
	var zero T
	return zero, false
}

// ClientCaps returns c's capabilities, looking through any wrapper
// layers (renamedClient, pollingUsageClient) via Unwrap(). Adding a
// capability is a struct field, not a new per-capability helper, and
// callers can never reintroduce the silent-false wrapper gap because
// there is no per-cap assertion to get wrong.
func ClientCaps(c Client) ClientCapabilities {
	if cp, ok := clientAs[capabilityProvider](c); ok {
		return cp.Capabilities()
	}
	return ClientCapabilities{}
}

// ClientMirrorsToolImages is a named convenience over ClientCaps for
// the agent loop's tool-image-mirror decision.
func ClientMirrorsToolImages(c Client) bool {
	return ClientCaps(c).MirrorsToolImages
}

// ClientContinuesAssistantPrefill is a named convenience over ClientCaps for
// the Stage "continue" gate: whether this client's wire format extends a
// trailing assistant message (a prefill) rather than starting a fresh turn.
func ClientContinuesAssistantPrefill(c Client) bool {
	return ClientCaps(c).ContinuesAssistantPrefill
}
