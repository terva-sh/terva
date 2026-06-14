package sdk

import (
	"encoding/json"

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

func rebuildContent(blocks []ContentBlock) []provider.Content {
	out := make([]provider.Content, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, provider.TextBlock{Text: b.Text})
		case "image":
			out = append(out, provider.ImageBlock{MimeType: b.MimeType, Data: b.Data})
		case "tool_call":
			args := b.Args
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			out = append(out, provider.ToolCallBlock{ID: b.ID, Name: b.Name, Arguments: args})
		case "tool_result":
			out = append(out, provider.ToolResultBlock{
				CallID:  b.CallID,
				IsError: b.IsError,
				Content: rebuildContent(b.Content),
			})
		case "reasoning":
			out = append(out, provider.ReasoningBlock{
				ID:        b.ReasoningID,
				Summary:   b.Summary,
				Encrypted: b.Encrypted,
			})
		}
	}
	return out
}
