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
	// tool_result: files this call published for the user (see [SharedFile]).
	// First-class for the same reason the line counts are — ToolResult.Details
	// never reaches the wire, and a card the remote panel cannot see is the
	// whole feature missing.
	Shared []SharedFile `json:"shared,omitempty"`

	// usage
	Usage      *WireUsage `json:"usage,omitempty"`
	Cumulative *WireUsage `json:"cumulative,omitempty"`

	// user_message / assistant_message
	Message *WireMessage `json:"message,omitempty"`

	// turn_end
	Stop string `json:"stop,omitempty"`

	// turn_end (failed) and error
	Error string `json:"error,omitempty"`

	// user_message_rejected: the blocked prompt itself, IN FULL (the reason
	// rides Text). Clients quote a truncated stub today; carrying the whole
	// text is what lets a restore-to-composer client exist later without a
	// wire change.
	Rejected string `json:"rejected,omitempty"`

	// stall / escalation (the stuck-loop hatch's live events). Nested rather
	// than flattened: each carries several fields, and keeping them self-contained
	// leaves the flat bag above readable — the same reason Message and Usage nest.
	Stall      *WireStall      `json:"stall,omitempty"`
	Escalation *WireEscalation `json:"escalation,omitempty"`
	Retry      *WireRetry      `json:"retry,omitempty"`
}

// WireRetry is the payload of a "retry" event: a turn attempt hit a transient
// provider error and the agent is waiting before trying again.
//
// delay_ms rather than a duration string because every client that renders it
// does arithmetic on it (a countdown, a spinner label), and a client that only
// prints it can format a number. attempt is 1-based and counts the attempt that
// just failed, so attempt/max renders directly as "2 of 6".
type WireRetry struct {
	Provider string `json:"provider,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Max      int    `json:"max,omitempty"`
	DelayMS  int64  `json:"delay_ms,omitempty"`
	Error    string `json:"error,omitempty"`
}

// WireStall is the payload of a "stall" event: the stuck-loop detector acted on
// a loop. axis is which axis caught it, tool the tool it looped on, detail the
// repeated error slice (churn only) or the reason the turn ended (rung 4).
//
// rung says WHAT it did — 1 nudge, 2 hold-off, 3 refused to dispatch the call,
// 4 ended the turn — and a renderer needs it, because those are four different
// things to tell a user, not four intensities of the same one. Omitted when 1,
// which is also what an older peer sends, so absent reads as the nudge.
type WireStall struct {
	Axis   string `json:"axis,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
	Rung   int    `json:"rung,omitempty"`
}

// WireEscalation is the payload of an "escalation" event: rung 3 resolved.
// disposition ∈ switched|declined|stopped|failed; on a switch, to_provider/
// to_model name where the session went (egress the operator should see).
type WireEscalation struct {
	Reason      string `json:"reason,omitempty"`
	Tool        string `json:"tool,omitempty"`
	FromModel   string `json:"from_model,omitempty"`
	ToProvider  string `json:"to_provider,omitempty"`
	ToModel     string `json:"to_model,omitempty"`
	Auto        bool   `json:"auto,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Detail      string `json:"detail,omitempty"` // failure error or host note (e.g. why a swap failed)
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

// MetaSource is the meta key naming what produced a message when it was not an
// ordinary model turn — "card:greeting" for a seeded opening, MetaDirected for a
// user-directed line (below). MetaActor carries the speaking character's name on
// a directed line.
const (
	MetaSource = "source"
	MetaActor  = "actor"
)

// MetaDirected is the MetaSource value for a message the user AUTHORED into the
// scene via Stage's directed-authorship surface (Phase 6) — a character's or the
// narrator's line they drafted, approved, and posted at their reply boundary —
// rather than one the model produced on its own turn. Its MetaActor names the
// speaking character (absent/empty means a narrator beat). A display surface
// renders it with the 🎭 attribution the cast machinery uses, not as an ordinary
// model turn; the provider request builders merge it into any adjacent assistant
// message (MergeAdjacentSameRole), so a directed line beside a real turn is one
// turn on the wire.
const MetaDirected = "stage:directed"

// MetaRouted is the MetaSource value for a line the meta-narrator ROUTED (the
// Worlds W3 coordinator): on a normal user turn in a World with several
// characters, the router picked this speaker and their line was generated in
// their voice — model-produced, unlike a MetaDirected line the user authored.
// Its MetaActor names the speaking character (absent/empty means a narrator
// beat). Renders with the same 🎭 attribution as a directed line; the provider
// request builders likewise merge it into any adjacent assistant message.
const MetaRouted = "stage:routed"

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

// MetaPreamble marks a message whose FIRST content block is a host-assembled
// preamble rather than anything the user typed (see [UserMessageExtras]).
//
// It is stamped by the one place that can know — the prompt path that prepends
// the block — so every reader of "the user's message" can skip it without
// guessing from the text. Two do: the client, which would otherwise render nine
// lines of machine prose above two lines of question, and the session-title
// seed, which would otherwise name the session after a $TERVA_HOME path.
//
// Deliberately NOT inferred from [MetaAttachments]. That was the first shape of
// this, and it broke in the one case the feature was designed around: when
// every attachment a message named had already been swept, the preamble was
// still emitted (to tell the model what it was not getting) while the
// attachment list was empty — so both readers stopped skipping, and the prose
// they exist to hide became the user's bubble and the session's title. A
// preamble is a fact about the content; attachments are a fact about the files.
const MetaPreamble = "preamble"

// MetaAttachments records the files a user attached to this message, as a JSON
// array of [WireAttachment]. It is DESCRIPTIVE, not a handle: the model is told
// where to read the files by the message's preamble block, and this exists so a
// client can render what was attached without showing that preamble's machine
// prose (an absolute staging path wraps to nine lines on a phone).
//
// It lists only what RESOLVED — a label for a file the daemon could not find
// would be a claim it cannot support. What went missing is counted in
// [MetaAttachmentsMissing] instead.
//
// The record deliberately outlives the files. A staged attachment is swept on a
// TTL, so a message reopened days later still says what was attached and simply
// cannot offer it — which is why the client renders these as inert labels and
// not as anything clickable.
const MetaAttachments = "attachments"

// MetaAttachmentsMissing counts the attachments a message named that no longer
// resolved when it was sent, as a decimal string.
//
// The model is told this in the preamble, and the client needs it for the same
// reason: a send whose files had all expired must not render as a message with
// no attachments at all. That is what "suppress the preamble" would otherwise
// turn it into — the user attaches two files, both lapse, and the panel shows a
// bare question with nothing to explain why the answer ignores them.
const MetaAttachmentsMissing = "attachments_missing"

// WireAttachment describes one file a user attached to a message: enough for a
// client to label it, and nothing more. No path and no id — the file is not
// retrievable from here, deliberately. Sending files the other way (agent to
// user) is [SharedFile], which is a separate flow and deliberately not this
// shape: an inbound label is inert, an outbound one is a link.
type WireAttachment struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // image | audio | video | document
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// MetaShared records the files a tool call in this message published FOR the
// user, as a JSON array of [SharedFile]. It rides the tool-role message rather
// than living in a store of its own, which is what makes history paging,
// conversation.reveal, and resume place each card correctly for free — the
// record travels with the turn that produced it and there is nothing to join.
//
// The model never sees this. Its copy is the tool result's text line ("shared
// report.pdf with the user"), which is all it needs to refer to the file in
// prose; the record is for the client, and putting a retrievable handle in the
// transcript would put it in every subsequent request.
const MetaShared = "shared"

// SharedFile is one file the agent published for the user to look at, play, or
// download — the outbound counterpart to [WireAttachment], and unlike it, a
// handle: ID resolves against the daemon's share store, and the web panel builds
// a download URL from it.
//
// It is one type across three surfaces — [ToolResult.Shared], [WireEvent.Shared]
// on a live tool_result, and [WireMessage.Shared] on replay — because it is the
// same record at each, and a conversion between identical shapes is only a place
// for them to drift apart.
//
// CallID is stamped by the agent loop, not by the tool: one tool-role message
// can carry results from several calls, and the client needs to know which card
// belongs to which. A tool has no way to know its own call id, which is also why
// it has no way to claim someone else's.
type SharedFile struct {
	ID      string `json:"id"`
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // image | audio | video | document
	Mime    string `json:"mime,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Caption string `json:"caption,omitempty"`
	// ExpiresAt (RFC3339) is when the share store's sweeper becomes entitled to
	// remove the bytes. It is what lets a client stop offering a download it
	// knows will fail, for EVERY kind of file — the alternative is inferring
	// liveness from a failed request, which only works where there is an element
	// that fails, i.e. an image, i.e. one kind of four.
	//
	// An upper bound, not a promise: cap eviction can take a file earlier, so a
	// download may still 404 before this. The record is deliberately kept in the
	// transcript afterwards — a message reopened next year still says what was
	// shared, it just no longer pretends to hand it over.
	//
	// Empty from a daemon that does not send it; a client must treat that as
	// "unknown", not as "expired".
	ExpiresAt string `json:"expires_at,omitempty"`
}

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
	// Directed is true for an assistant message the user authored into the scene
	// via directed authorship (Phase 6) — a character's or narrator's line they
	// drafted and posted — rather than a model turn. Routed is true for a line
	// the meta-narrator routed to a character on a normal turn (Worlds W3) —
	// model-produced, in that character's voice. Actor is the speaking
	// character's name for either; empty means the narrator. A client renders
	// them with 🎭 attribution instead of as an ordinary assistant bubble. Typed
	// fields and not the raw Meta map, for the reason Synthetic/Compaction give
	// above.
	Directed bool   `json:"directed,omitempty"`
	Routed   bool   `json:"routed,omitempty"`
	Actor    string `json:"actor,omitempty"`
	// Attachments are the files the user attached to this message and that still
	// resolved when it was sent (see [MetaAttachments]). A client renders them
	// as inert labels beside the message.
	//
	// Typed, like every field above, for the reason stated there.
	Attachments []WireAttachment `json:"attachments,omitempty"`
	// AttachmentsMissing counts the attachments this message named that had
	// already been swept (see [MetaAttachmentsMissing]). A client says so rather
	// than rendering a message that looks like it carried nothing.
	AttachmentsMissing int `json:"attachments_missing,omitempty"`
	// Preamble reports that Content[0] is the host's own words, not the user's
	// (see [MetaPreamble]) — the block naming the attachments' absolute staging
	// paths, which the model needs and a human reads as noise. A client drops
	// that block and renders the fields above in its place.
	//
	// Its own field rather than something inferred from Attachments: a message
	// can carry a preamble with nothing left to label, which is exactly when
	// inferring it went wrong.
	Preamble bool `json:"preamble,omitempty"`
	// Shared are the files tool calls in this message published for the user
	// (see [MetaShared]). Present only on a tool-role message, and each entry
	// names the call it came from, so a client can put the card beside the right
	// tool row — or, better, outside it: a tool group renders collapsed, and a
	// download the user cannot see is a download that did not happen.
	Shared []SharedFile `json:"shared,omitempty"`
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

	// compaction_summary — who issued the encrypted blob. Only that provider
	// can replay it, and a blob that arrives without provenance is treated as
	// replayable by nobody, so this must survive the wire round-trip or a
	// client hands back a compaction that looks foreign to its own issuer.
	Provider string `json:"provider,omitempty"`
}

// WireUsage is per-turn or cumulative token / cost counts.
type WireUsage struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
	// CacheSavedUSD is what the prompt cache was worth on these tokens, priced
	// per response by the model that answered it (provider.ApplyCost). Signed:
	// negative means cache writes outran the reads they bought.
	CacheSavedUSD float64 `json:"cache_saved_usd,omitempty"`
	// Reasoning is how much of Output the model spent thinking — a SUBSET of
	// Output, not a fifth bucket, since it is billed at the output rate.
	// ReasoningKnown separates a reported zero from a provider that does not
	// break reasoning out at all (Anthropic keeps it inside output_tokens).
	Reasoning      int  `json:"reasoning,omitempty"`
	ReasoningKnown bool `json:"reasoning_known,omitempty"`
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
		out.Shared = e.Result.Shared
	case EvUsage:
		u := usageToWire(e.Usage)
		c := usageToWire(e.Cumulative)
		out.Usage = &u
		out.Cumulative = &c
	case EvUserMessageRejected:
		// A BeforeUserMessage guard refused the prompt before it reached the
		// model. The reason is the human-facing "why", so it rides Text (the
		// same field EvCompactStart's reason uses); the refused prompt rides
		// Rejected so a client can quote it — a reason with no words attached
		// stops meaning anything once the user forgets what they typed.
		// Without this event the rejection vanished on every non-in-process
		// surface (web, --json).
		out.Text = e.Reason
		out.Rejected = e.Text
	case EvUserMessageWithdrawn:
		// The withdrawn prompt itself, in full, on Text. Unlike a rejection
		// there is no second string to carry — nobody supplied a reason, the
		// user simply cancelled — so the prompt takes the plain field rather
		// than borrowing Rejected, which would report a withdrawal as something
		// that had been refused.
		//
		// Images are deliberately absent: raw bytes do not ride this wire, and a
		// remote client that restored text while silently dropping attachments
		// would be worse than one that knows it only has the text.
		out.Text = e.Text
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
	case EvStall:
		out.Stall = &WireStall{Axis: e.Axis, Tool: e.Tool, Detail: e.Detail, Rung: e.Rung}
	case EvRetry:
		out.Retry = &WireRetry{
			Provider: e.Provider,
			Attempt:  e.Attempt,
			Max:      e.Max,
			DelayMS:  e.Delay.Milliseconds(),
			Error:    e.Err,
		}
	case EvEscalation:
		out.Escalation = &WireEscalation{
			Reason:      e.Reason,
			Tool:        e.Tool,
			FromModel:   e.FromModel,
			ToProvider:  e.ToProvider,
			ToModel:     e.ToModel,
			Auto:        e.Auto,
			Disposition: string(e.Disposition),
			Detail:      e.Detail,
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
	switch m.Meta[MetaSource] {
	case MetaDirected:
		w.Directed = true
		w.Actor = m.Meta[MetaActor] // empty = narrator
	case MetaRouted:
		w.Routed = true
		w.Actor = m.Meta[MetaActor] // empty = narrator
	}
	// A malformed list or count is not worth failing a transcript over — the
	// message still renders, just without its labels.
	if raw := m.Meta[MetaAttachments]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &w.Attachments)
	}
	w.AttachmentsMissing, _ = strconv.Atoi(m.Meta[MetaAttachmentsMissing])
	// Read independently of the two above: a preamble survives its attachments.
	w.Preamble = m.Meta[MetaPreamble] == "true"
	// Same tolerance, same reason: a share whose record failed to parse costs the
	// user a download card, and failing the whole transcript over it would cost
	// them the conversation.
	if raw := m.Meta[MetaShared]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &w.Shared)
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
		case provider.CompactionBlock:
			// Carried over ctrlproto for the same reason reasoning is: the
			// blob must survive a round-trip through the TUI/web backend or
			// the next request replays a compaction with its contents gone.
			out = append(out, WireBlock{
				Type:      "compaction_summary",
				ID:        v.ID,
				Encrypted: v.Encrypted,
				Provider:  v.Provider,
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
	if w.Directed {
		setMeta(MetaSource, MetaDirected)
		if w.Actor != "" {
			setMeta(MetaActor, w.Actor)
		}
	}
	if w.Routed {
		setMeta(MetaSource, MetaRouted)
		if w.Actor != "" {
			setMeta(MetaActor, w.Actor)
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
		case "compaction_summary":
			out = append(out, provider.CompactionBlock{ID: b.ID, Encrypted: b.Encrypted, Provider: b.Provider})
		}
	}
	return out
}

// UsageToWire converts a provider usage record to its wire form.
//
// Exported because the workspace kept a byte-identical private twin of it, and
// a twin is how a field gets added to one wire path and not the other: the
// control plane's status payloads went through the copy while the event stream
// went through this one. One converter, both callers.
func UsageToWire(u provider.Usage) WireUsage {
	return WireUsage{
		Input:          u.InputTokens,
		Output:         u.OutputTokens,
		CacheRead:      u.CacheReadTokens,
		CacheWrite:     u.CacheWriteTokens,
		CostUSD:        u.CostUSD,
		CacheSavedUSD:  u.CacheSavedUSD,
		Reasoning:      u.ReasoningTokens,
		ReasoningKnown: u.ReasoningTokensKnown,
	}
}

func usageToWire(u provider.Usage) WireUsage { return UsageToWire(u) }
