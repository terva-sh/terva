// Package provider defines the LLM client abstraction used by terva.
//
// It supports exactly two providers: Anthropic (Messages API) and
// OpenAI (Chat Completions API). Everything above this package operates
// on the types declared here and does not know about HTTP or SSE.
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
}

func (ImageBlock) isContent() {}

// ToolCallBlock is an assistant-issued call to a tool.
type ToolCallBlock struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
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
type Usage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// Add returns u plus v.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + v.InputTokens,
		OutputTokens:     u.OutputTokens + v.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
		CostUSD:          u.CostUSD + v.CostUSD,
	}
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
	// Reasoning is "", "minimum", "low", "medium", "high", or "maximum".
	// Empty disables reasoning. Budget-based providers map these to roughly
	// 1k/2k/8k/16k/32k thinking tokens; effort-based providers map them onto
	// their closest supported reasoning_effort values.
	Reasoning string

	// EphemeralContext is host-assembled, host-wrapped text injected into
	// the model's context for THIS request only — never part of Messages
	// and never persisted to the transcript. Providers append it as a
	// trailing message AFTER the cache breakpoint so the cached prefix
	// (system + tools + history) still hits and only this block is
	// re-processed. Used for standing context that changes between turns
	// (e.g. an extension's live task card). Empty means inject nothing.
	EphemeralContext string
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
// RefreshingClient, renamedClient). Capability probes use it to see
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
// client (the MirrorsToolImages bug class): codex ships wrapped in a
// RefreshingClient and openai-responses/google-vertex in a
// renamedClient, so an inline assertion on the outer client missed
// the capability entirely.
type ClientCapabilities struct {
	// MirrorsToolImages is true for wire formats that can't carry
	// images inside a tool result (OpenAI chat-completions; the
	// Responses/codex function_call_output). The agent loop mirrors
	// such images into a following synthetic user message. This is a
	// WIRE-FORMAT fact; whether the current model can see images at
	// all is the separate per-model image-input capability the loop
	// checks alongside it.
	MirrorsToolImages bool
}

// capabilityProvider is implemented by concrete clients that declare
// capabilities. Wrappers must NOT implement it — they expose Unwrap()
// and ClientCaps walks down to the concrete client.
type capabilityProvider interface {
	Capabilities() ClientCapabilities
}

// ClientCaps returns c's capabilities, looking through any wrapper
// layers (RefreshingClient, renamedClient) via Unwrap(). The unwrap
// walk lives here, once: adding a capability is a struct field, not a
// new per-capability helper, and callers can never reintroduce the
// silent-false wrapper gap because there is no per-cap assertion to
// get wrong.
func ClientCaps(c Client) ClientCapabilities {
	for c != nil {
		if cp, ok := c.(capabilityProvider); ok {
			return cp.Capabilities()
		}
		u, ok := c.(unwrapper)
		if !ok {
			return ClientCapabilities{}
		}
		c = u.Unwrap()
	}
	return ClientCapabilities{}
}

// ClientMirrorsToolImages is a named convenience over ClientCaps for
// the agent loop's tool-image-mirror decision.
func ClientMirrorsToolImages(c Client) bool {
	return ClientCaps(c).MirrorsToolImages
}
