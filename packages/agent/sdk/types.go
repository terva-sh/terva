package sdk

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Event is the unit emitted by Runtime.Prompt's channel. It is an
// alias of core.WireEvent — the canonical wire schema shared verbatim
// by `terva --json`, the RPC event stream, and swarm event logs — so
// consumers can share parsing code across all of them. Discriminate
// on Type; the remaining fields populate by kind (see core.WireEvent).
type Event = core.WireEvent

// Message is one transcript entry — a user prompt, assistant reply,
// or tool result. Alias of the canonical wire form.
type Message = core.WireMessage

// ContentBlock is one piece of message content; discriminate on Type
// ("text", "image", "tool_call", "tool_result"). Alias of the
// canonical wire form.
type ContentBlock = core.WireBlock

// Image is a single user-attached image for Prompt.
type Image struct {
	MimeType string
	Data     []byte
}

// Usage is per-turn or cumulative token / cost counts. Alias of the
// canonical wire form.
type Usage = core.WireUsage

// State is a snapshot of the runtime's current state.
type State struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	CWD          string `json:"cwd"`
	Busy         bool   `json:"busy"`
	MessageCount int    `json:"message_count"`
}

// CompactResult describes the outcome of Compact.
type CompactResult struct {
	Summary  string    `json:"summary"`
	Messages []Message `json:"messages"`
}

// ModelInfo describes one model known to the runtime.
type ModelInfo struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window"`
	MaxOutput     int    `json:"max_output"`
	Reasoning     bool   `json:"reasoning"`
}

// ---- internal converters ----
// rebuildContent decodes wire blocks back into provider.Content.
//
// It is core.ContentFromWire and nothing else. It used to be a private
// second implementation of the same switch, and it had already drifted: it
// handled five of the six content blocks and silently dropped
// compaction_summary, so an embedder that read a transcript through
// Messages() and handed it back lost the backend's only encoding of
// everything a compaction replaced -- a blob terva cannot rebuild.
//
// ContentBlock is a type ALIAS of core.WireBlock, so the two decoders had
// identical signatures over identical input the whole time. There was never
// a reason for the copy; keeping the name as a thin call preserves the
// package-local vocabulary without keeping a second switch to forget.
func rebuildContent(blocks []ContentBlock) []provider.Content {
	return core.ContentFromWire(blocks)
}
