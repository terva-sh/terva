package core

import (
	"encoding/json"
	"strconv"
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
	// tool_result line-change counts (the status bar's Δ segment)
	LinesAdded   int `json:"lines_added,omitempty"`
	LinesRemoved int `json:"lines_removed,omitempty"`

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

// MetaSynthetic marks a user-role message the host injected (the at-close
// continuation-gate nudge) rather than one the user typed. Display surfaces
// de-emphasize it; extension observers already skip it (EvUserMessage.Synthetic).
const MetaSynthetic = "synthetic"

// MetaCompaction marks the synthetic summary a compaction checkpoint left in
// place of the turns it folded away, and MetaTokensBefore carries that
// checkpoint's estimate of the transcript size it replaced. Set in Compact;
// read by every display surface, which renders the message as a divider rather
// than as the user message its RoleUser would otherwise imply.
const (
	MetaCompaction   = "compaction"
	MetaTokensBefore = "tokens_before"
)

// MetaClear marks a DISPLAY-ONLY divider standing in for a /clear checkpoint.
//
// Unlike a compaction, a clear leaves no message behind — it is an empty checkpoint
// (AppendCompaction(nil)) — so there is nothing in the transcript for a renderer to
// draw a boundary on, and the conversation above it would otherwise just stop with
// no explanation. A client that pages history back through one MINTS this marker to
// mark the spot.
//
// It is never in a model transcript and never crosses the wire as a message: it is
// synthesized by the client, lives only in what that client paints, and is not in
// the agent's message list. Values: "true" while the clear still stands between you
// and the conversation before it, "crossed" once you have chosen to look anyway.
const MetaClear = "clear"

// WireMessage is one transcript entry on the wire.
type WireMessage struct {
	Role    string      `json:"role"`
	Content []WireBlock `json:"content"`
	Time    string      `json:"time,omitempty"` // RFC 3339
	// Synthetic is true for a host-injected message (not the user's words), so a
	// client can render it as a system note instead of a user bubble.
	Synthetic bool `json:"synthetic,omitempty"`
	// Compaction is true for the summary a compaction checkpoint left behind,
	// and TokensBefore is that checkpoint's estimate of what it replaced. A
	// client renders the pair as a divider ("compacted here, ~N tokens") with
	// the summary collapsed behind it — not as a user bubble full of raw
	// "## Context Summary" markdown, which is what every frontend showed while
	// these fields did not exist and provider.Message.Meta could not cross.
	//
	// Deliberately two typed fields and not the whole Meta map: Meta is an open
	// bag, and shipping it wholesale would make every key anyone ever adds to it
	// into protocol — including keys we did not mean to hand a client.
	Compaction   bool `json:"compaction,omitempty"`
	TokensBefore int  `json:"tokens_before,omitempty"`
}

// WireBlock is one piece of message content. Discriminate on Type:
//   - "image"       → MimeType + Bytes, and — only on the Full
//     conversions — Data. The lean conversions (EventToWire,
//     MessageToWire) carry size only: they feed --json output, swarm
//     event logs, and the legacy RPC, where inlined payloads are
//     bloat. The control plane broadcasts the Full form (free
//     in-process; the TUI carrier renders real pixels) and serialized
//     carriers strip Data at the connection boundary unless the
//     client negotiated the "image-data" feature. Data also serves
//     INBOUND payloads (SDK SetMessages).
//   - "text"        → Text
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

// EventToWire converts an AgentEvent to its canonical wire form. Image
// blocks carry size only — right for --json output, swarm event logs, and
// the legacy RPC, where inlined payloads are bloat. The control plane
// broadcasts EventToWireFull instead.
func EventToWire(ev AgentEvent) WireEvent { return eventToWire(ev, false) }

// EventToWireFull is EventToWire with image payloads included (Data
// alongside the usual MimeType+Bytes). The workspace hub broadcasts this
// form: an in-process subscriber (the TUI carrier) gets real pixels for
// free — the Data slices are shared, not copied — and serialized carriers
// strip Data at the connection boundary unless the client negotiated the
// "image-data" feature.
func EventToWireFull(ev AgentEvent) WireEvent { return eventToWire(ev, true) }

func eventToWire(ev AgentEvent, imageData bool) WireEvent {
	out := WireEvent{Type: ev.Type()}
	switch e := ev.(type) {
	case EvTurnStart:
		out.Step = e.Step
	case EvUserMessage:
		m := messageToWire(e.Message, imageData)
		out.Message = &m
	case EvAssistantMessage:
		m := messageToWire(e.Message, imageData)
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
		out.Result = contentToWire(e.Result.Content, imageData)
		out.LinesAdded = e.Result.LinesAdded
		out.LinesRemoved = e.Result.LinesRemoved
	case EvUsage:
		u := usageToWire(e.Usage)
		c := usageToWire(e.Cumulative)
		out.Usage = &u
		out.Cumulative = &c
	case EvUserMessageRejected:
		// A BeforeUserMessage guard refused the prompt before it reached the
		// model. The reason is the human-facing "why", so it rides Text (the
		// same field EvCompactStart's reason uses); a wire client shows it in
		// the conversation area. Without this the rejection vanished on every
		// non-in-process surface (web, --json).
		out.Text = e.Reason
	case EvTurnEnd:
		out.Stop = string(e.Stop)
		if e.Err != nil {
			out.Error = e.Err.Error()
		}
	case EvCompactStart:
		out.Text = e.Reason
	case EvCompactEnd:
		out.Error = e.Err
		// What the condense itself cost. Rides the same Usage field EvUsage
		// uses, but on a compact_end frame — so a client that seeds a context
		// gauge from usage (they key off EvUsage) can't mistake it for one.
		u := usageToWire(e.Usage)
		out.Usage = &u
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

// MessageToWire converts one transcript message to its wire form (image
// blocks size-only; see EventToWire).
func MessageToWire(m provider.Message) WireMessage { return messageToWire(m, false) }

// MessageToWireFull is MessageToWire with image payloads included — the
// form control-plane snapshots carry (see EventToWireFull).
func MessageToWireFull(m provider.Message) WireMessage { return messageToWire(m, true) }

func messageToWire(m provider.Message, imageData bool) WireMessage {
	w := WireMessage{Role: string(m.Role), Content: contentToWire(m.Content, imageData)}
	if !m.Time.IsZero() {
		w.Time = m.Time.Format(time.RFC3339Nano)
	}
	if m.Meta[MetaSynthetic] == "true" {
		w.Synthetic = true
	}
	if m.Meta[MetaCompaction] == "true" {
		w.Compaction = true
		// A malformed count is not worth failing a transcript over: the divider
		// renders without it.
		w.TokensBefore, _ = strconv.Atoi(m.Meta[MetaTokensBefore])
	}
	return w
}

// ContentToWire converts transcript content blocks to wire form (image
// blocks size-only; see EventToWire).
func ContentToWire(blocks []provider.Content) []WireBlock { return contentToWire(blocks, false) }

// ContentToWireFull is ContentToWire with image payloads included (see
// EventToWireFull).
func ContentToWireFull(blocks []provider.Content) []WireBlock { return contentToWire(blocks, true) }

func contentToWire(blocks []provider.Content, imageData bool) []WireBlock {
	out := make([]WireBlock, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.(type) {
		case provider.TextBlock:
			out = append(out, WireBlock{Type: "text", Text: v.Text})
		case provider.ImageBlock:
			// The full form keeps Bytes too, so stripping Data at a carrier
			// boundary yields exactly the lean shape.
			w := WireBlock{Type: "image", MimeType: v.MimeType, Bytes: len(v.Data)}
			if imageData {
				w.Data = v.Data
			}
			out = append(out, w)
		case provider.ToolCallBlock:
			out = append(out, WireBlock{Type: "tool_call", ID: v.ID, Name: v.Name, Args: v.Arguments})
		case provider.ToolResultBlock:
			out = append(out, WireBlock{
				Type:    "tool_result",
				CallID:  v.CallID,
				IsError: v.IsError,
				Content: contentToWire(v.Content, imageData),
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

// MessageFromWire rebuilds a transcript message from its wire form — the
// inverse of MessageToWire, for clients that render a transcript received
// over the control plane (ctrlproto snapshots + message events). Image
// blocks keep whatever Data the carrier delivered: real pixels from the
// full form (in-process, or a serialized carrier that negotiated
// "image-data"), or none from the lean form — renderers then fall back to
// their metadata line.
func MessageFromWire(w WireMessage) provider.Message {
	m := provider.Message{Role: provider.Role(w.Role), Content: ContentFromWire(w.Content)}
	if w.Time != "" {
		if t, err := time.Parse(time.RFC3339Nano, w.Time); err == nil {
			m.Time = t
		}
	}
	setMeta := func(k, v string) {
		if m.Meta == nil {
			m.Meta = map[string]string{}
		}
		m.Meta[k] = v
	}
	if w.Synthetic {
		setMeta(MetaSynthetic, "true")
	}
	if w.Compaction {
		setMeta(MetaCompaction, "true")
		if w.TokensBefore > 0 {
			setMeta(MetaTokensBefore, strconv.Itoa(w.TokensBefore))
		}
	}
	return m
}

// ContentFromWire rebuilds transcript content blocks from wire form. Unknown
// block types (written by a newer terva) are skipped, mirroring the session
// loader's forward-compatibility rule.
func ContentFromWire(blocks []WireBlock) []provider.Content {
	out := make([]provider.Content, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, provider.TextBlock{Text: b.Text})
		case "image":
			out = append(out, provider.ImageBlock{MimeType: b.MimeType, Data: b.Data})
		case "tool_call":
			out = append(out, provider.ToolCallBlock{ID: b.ID, Name: b.Name, Arguments: b.Args})
		case "tool_result":
			out = append(out, provider.ToolResultBlock{
				CallID:  b.CallID,
				IsError: b.IsError,
				Content: ContentFromWire(b.Content),
			})
		case "reasoning":
			out = append(out, provider.ReasoningBlock{ID: b.ReasoningID, Summary: b.Summary, Encrypted: b.Encrypted})
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
