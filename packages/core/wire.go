package core

import (
	"encoding/json"
	"time"

	"terva.sh/terva/packages/provider"
)

// This file defines THE canonical JSON shape of agent events across
// every machine-readable surface: `terva --json`, the RPC event stream,
// the SDK's Event type (an alias of WireEvent), and swarm event logs.
// Before this existed there were three hand-maintained serializers
// that had already drifted (flat vs nested message shapes, missing
// fields, different error keys); docs/rpc.md claimed the RPC wire
// mirrored the SDK types "one-for-one", which was false. With one
// mapping it is a structural fact.
//
// Adding an AgentEvent? Extend EventToWire here and every surface
// picks it up. (Not to be confused with session.go's unexported
// wireMessage/wireBlock, which are the on-DISK transcript schema —
// that one carries raw image bytes; this one deliberately doesn't.)

// WireEvent is one serialized AgentEvent. Type identifies the kind;
// the remaining fields populate by kind.
type WireEvent struct {
	Type string `json:"type"`

	// turn_start
	Step int `json:"step,omitempty"`

	// text_delta and tool_use_args (argument-fragment streaming)
	Delta string `json:"delta,omitempty"`

	// tool_use_start / tool_use_end / tool_call / tool_progress / tool_result
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	Text string          `json:"text,omitempty"`

	// tool_result
	IsError bool        `json:"is_error,omitempty"`
	Result  []WireBlock `json:"content,omitempty"`

	// usage
	Usage      *WireUsage `json:"usage,omitempty"`
	Cumulative *WireUsage `json:"cumulative,omitempty"`

	// user_message / assistant_message
	Message *WireMessage `json:"message,omitempty"`

	// turn_end
	Stop string `json:"stop,omitempty"`

	// turn_end (failed) and error
	Error string `json:"error,omitempty"`
}

// WireMessage is one transcript entry on the wire.
type WireMessage struct {
	Role    string      `json:"role"`
	Content []WireBlock `json:"content"`
	Time    string      `json:"time,omitempty"` // RFC 3339
}

// WireBlock is one piece of message content. Discriminate on Type:
//   - "text"        → Text
//   - "image"       → MimeType + Bytes (size only; raw data does not
//     cross the event wire — Data exists for INBOUND payloads like
//     SDK SetMessages)
//   - "tool_call"   → ID + Name + Args
//   - "tool_result" → CallID + IsError + Content (recursive)
//   - "reasoning"   → ReasoningID + Summary + Encrypted (assistant
//     chain-of-thought metadata; some providers, e.g. OpenAI Codex with
//     thinking enabled, require the encrypted payload replayed on
//     follow-up requests, so it must survive a wire round-trip)
type WireBlock struct {
	Type string `json:"type"`

	Text     string          `json:"text,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
	Data     []byte          `json:"data,omitempty"`
	Bytes    int             `json:"bytes,omitempty"`
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	CallID   string          `json:"call_id,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	Content  []WireBlock     `json:"content,omitempty"`

	// reasoning
	ReasoningID string `json:"reasoning_id,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Encrypted   string `json:"encrypted_content,omitempty"`
}

// WireUsage is per-turn or cumulative token / cost counts.
type WireUsage struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
}

// EventToWire converts an AgentEvent to its canonical wire form.
func EventToWire(ev AgentEvent) WireEvent {
	out := WireEvent{Type: ev.Type()}
	switch e := ev.(type) {
	case EvTurnStart:
		out.Step = e.Step
	case EvUserMessage:
		m := MessageToWire(e.Message)
		out.Message = &m
	case EvAssistantMessage:
		m := MessageToWire(e.Message)
		out.Message = &m
	case EvTextDelta:
		out.Delta = e.Delta
	case EvToolUseStart:
		out.ID = e.ID
		out.Name = e.Name
	case EvToolUseArgs:
		out.ID = e.ID
		out.Delta = e.Delta
	case EvToolUseEnd:
		out.ID = e.ID
	case EvToolCall:
		out.ID = e.ID
		out.Name = e.Name
		out.Args = e.Args
	case EvToolProgress:
		out.ID = e.ID
		out.Text = e.Text
	case EvToolResult:
		out.ID = e.ID
		out.IsError = e.Result.IsError
		out.Result = ContentToWire(e.Result.Content)
	case EvUsage:
		u := usageToWire(e.Usage)
		c := usageToWire(e.Cumulative)
		out.Usage = &u
		out.Cumulative = &c
	case EvTurnEnd:
		out.Stop = string(e.Stop)
		if e.Err != nil {
			out.Error = e.Err.Error()
		}
	case EvCompactStart:
		out.Text = e.Reason
	case EvCompactEnd:
		out.Error = e.Err
	case EvError:
		if e.Err != nil {
			out.Error = e.Err.Error()
		}
	}
	return out
}

// Map renders the event as a generic map — for emitters that flatten
// fields into their own envelope (the swarm event log) or splice
// extra keys. The JSON bytes are identical either way.
func (e WireEvent) Map() map[string]any {
	b, err := json.Marshal(e)
	if err != nil {
		return map[string]any{"type": e.Type}
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{"type": e.Type}
	}
	return out
}

// MessageToWire converts one transcript message to its wire form.
func MessageToWire(m provider.Message) WireMessage {
	w := WireMessage{Role: string(m.Role), Content: ContentToWire(m.Content)}
	if !m.Time.IsZero() {
		w.Time = m.Time.Format(time.RFC3339Nano)
	}
	return w
}

// ContentToWire converts transcript content blocks to wire form.
// Image blocks carry size only — events are not a transport for raw
// image bytes.
func ContentToWire(blocks []provider.Content) []WireBlock {
	out := make([]WireBlock, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.(type) {
		case provider.TextBlock:
			out = append(out, WireBlock{Type: "text", Text: v.Text})
		case provider.ImageBlock:
			out = append(out, WireBlock{Type: "image", MimeType: v.MimeType, Bytes: len(v.Data)})
		case provider.ToolCallBlock:
			out = append(out, WireBlock{Type: "tool_call", ID: v.ID, Name: v.Name, Args: v.Arguments})
		case provider.ToolResultBlock:
			out = append(out, WireBlock{
				Type:    "tool_result",
				CallID:  v.CallID,
				IsError: v.IsError,
				Content: ContentToWire(v.Content),
			})
		case provider.ReasoningBlock:
			out = append(out, WireBlock{
				Type:        "reasoning",
				ReasoningID: v.ID,
				Summary:     v.Summary,
				Encrypted:   v.Encrypted,
			})
		}
	}
	return out
}

func usageToWire(u provider.Usage) WireUsage {
	return WireUsage{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadTokens,
		CacheWrite: u.CacheWriteTokens,
		CostUSD:    u.CostUSD,
	}
}
