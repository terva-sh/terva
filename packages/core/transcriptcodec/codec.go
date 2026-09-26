// Package transcriptcodec is how a transcript is written down and put back
// together, with no I/O: the JSON form of a provider.Message, the repair that
// gives an interrupted tool call a result, and the exclusion of an image a
// provider rejected. The engine uses it (its prefix watch digests messages
// through the same codec, so both agree on what a message is), and so does
// every store: packages/session writes the codec to JSONL, and
// core.MemoryTranscriptStore rebuilds a transcript with the same repair.
package transcriptcodec

import (
	"encoding/json"
	"time"

	"terva.sh/terva/packages/provider"
)

// WireMessage is the typed on-disk form of provider.Message. The
// outer shape (role/content/time/meta) is identical to v1; only the
// blocks gain a "type" field, so v1 readers (field presence, unknown
// fields ignored) read v2 files and vice versa.
type WireMessage struct {
	Role    provider.Role     `json:"role"`
	Content []WireBlock       `json:"content"`
	Time    time.Time         `json:"time"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// WireBlock is one typed content block. One flat struct (rather than
// per-kind types) keeps encoding/decoding a single switch; omitempty
// keeps each kind's row as small as v1's.
type WireBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"data,omitempty"`
	ImageID  string `json:"image_id,omitempty"` // ig_… generation id (assistant-emitted images), for edit replay
	// tool_call
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// RawArguments preserves argument text that never parsed. Without it the
	// row records a call with "{}" and the evidence of what the model actually
	// sent is gone — which is precisely what made the original defect hard to
	// read back out of a session.
	RawArguments string `json:"raw_arguments,omitempty"`
	// Signature is the provider's opaque token for the call (Gemini 3's
	// thoughtSignature). A resumed session replays its transcript, so losing
	// this on disk means every resumed Gemini 3 session 400s on its first turn.
	Signature string `json:"signature,omitempty"`
	// tool_result (Content nests text/image blocks)
	CallID  string      `json:"call_id,omitempty"`
	Content []WireBlock `json:"content,omitempty"`
	IsError bool        `json:"is_error,omitempty"`
	// compaction_summary — Provider names who issued the encrypted blob, and
	// only that provider can replay it. Its own field rather than borrowing
	// Name (the tool_call name): a reader of a session file should not have to
	// know which block type is being decoded to know what a field means.
	Provider string `json:"provider,omitempty"`
	// reasoning — Shape names the provider block this came off. A resumed
	// session replays its transcript, and an Anthropic thinking block is only
	// replayable to Anthropic, so losing this on disk turns a resumable turn
	// into one that is silently dropped from the request.
	ReasoningID string `json:"reasoning_id,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Encrypted   string `json:"encrypted_content,omitempty"`
	Shape       string `json:"shape,omitempty"`
}

// Block type discriminator values (WireBlock.Type).
const (
	blockText       = "text"
	blockImage      = "image"
	blockToolCall   = "tool_call"
	blockToolResult = "tool_result"
	blockReasoning  = "reasoning"
	// blockCompaction matches the provider's own wire name so a session file
	// and a request body read the same way side by side.
	blockCompaction = "compaction_summary"
)

// EncodeMessage converts a provider.Message to its typed on-disk
// form. Unknown in-memory block kinds are impossible today (Content
// is a closed set); if one appears it is dropped here at write time,
// which is loud in tests rather than silent at read time.
func EncodeMessage(m provider.Message) WireMessage {
	w := WireMessage{Role: m.Role, Time: m.Time, Meta: m.Meta}
	w.Content = EncodeBlocks(m.Content)
	return w
}

func EncodeBlocks(blocks []provider.Content) []WireBlock {
	out := make([]WireBlock, 0, len(blocks))
	for _, c := range blocks {
		switch b := c.(type) {
		case provider.TextBlock:
			out = append(out, WireBlock{Type: blockText, Text: b.Text})
		case provider.ImageBlock:
			out = append(out, WireBlock{Type: blockImage, MimeType: b.MimeType, Data: b.Data, ImageID: b.ID})
		case provider.ToolCallBlock:
			// Belt and braces on the invariant provider.FinalizeToolArguments
			// establishes. An invalid RawMessage does not corrupt one field: it
			// makes json.Marshal of the WHOLE message fail, returning zero
			// bytes, so AppendMessage errors and the assistant turn never
			// reaches disk while its tool_result does — leaving an orphan
			// result no reader can attribute. ToolCallBlock is also built
			// outside the provider package (the SDK, tests, replay), so the
			// row's writability is guaranteed here rather than assumed of every
			// producer. The original text moves to RawArguments instead of
			// being dropped, because it is the only record of what was sent.
			args, rawArgs := b.Arguments, b.RawArguments
			if len(args) == 0 || !json.Valid(args) {
				if rawArgs == "" {
					rawArgs = string(args)
				}
				args = json.RawMessage("{}")
			}
			out = append(out, WireBlock{Type: blockToolCall, ID: b.ID, Name: b.Name, Arguments: args, RawArguments: rawArgs, Signature: b.Signature})
		case provider.ToolResultBlock:
			out = append(out, WireBlock{
				Type:    blockToolResult,
				CallID:  b.CallID,
				Content: EncodeBlocks(b.Content),
				IsError: b.IsError,
			})
		case provider.ReasoningBlock:
			out = append(out, WireBlock{Type: blockReasoning, ReasoningID: b.ID, Summary: b.Summary, Encrypted: b.Encrypted, Shape: b.Shape})
		case provider.CompactionBlock:
			// Losing this block loses the compaction itself: the blob is the
			// backend's only encoding of the turns it replaced, and terva
			// cannot rebuild one. A resume that dropped it would silently
			// resume a conversation with a hole where its history was.
			out = append(out, WireBlock{Type: blockCompaction, ID: b.ID, Encrypted: b.Encrypted, Provider: b.Provider})
		}
	}
	return out
}

// DecodeBlock rebuilds one v2 typed block. ok=false means the
// type is unrecognized (written by a newer terva) — the caller records
// it and skips, rather than degrading it to an empty text block.
func DecodeBlock(b WireBlock) (provider.Content, bool) {
	switch b.Type {
	case blockText:
		return provider.TextBlock{Text: b.Text}, true
	case blockImage:
		return provider.ImageBlock{MimeType: b.MimeType, Data: b.Data, ID: b.ImageID}, true
	case blockToolCall:
		return provider.ToolCallBlock{ID: b.ID, Name: b.Name, Arguments: b.Arguments, RawArguments: b.RawArguments, Signature: b.Signature}, true
	case blockToolResult:
		block := provider.ToolResultBlock{CallID: b.CallID, IsError: b.IsError}
		for _, inner := range b.Content {
			if c, ok := DecodeBlock(inner); ok {
				block.Content = append(block.Content, c)
			}
		}
		return block, true
	case blockReasoning:
		return provider.NormalizeLegacyReasoningShape(provider.ReasoningBlock{
			ID: b.ReasoningID, Summary: b.Summary, Encrypted: b.Encrypted, Shape: b.Shape,
		}), true
	case blockCompaction:
		return provider.CompactionBlock{ID: b.ID, Encrypted: b.Encrypted, Provider: b.Provider}, true
	}
	return nil, false
}
