// Package extproto defines the JSON-over-stdin/stdout wire format
// spoken between zot and its extension subprocesses. Both the host
// (packages/agent/extensions) and the SDK (packages/agent/ext) marshal/
// unmarshal the same types, so changes here ripple through both.
//
// All frames are one JSON object terminated by a single LF. Object
// boundaries follow newline boundaries; no multi-line JSON.
//
// Direction conventions in this file:
//   - Type names ending in "FromExt" are sent by the extension to zot.
//   - Type names ending in "FromHost" are sent by zot to the extension.
//   - Names without a suffix are direction-neutral payloads or shared
//     value types.
//
// Every frame has a top-level Type discriminator. Optional ID is
// present on commands and on responses to commands so the sender can
// correlate; events and notifications never carry an ID.
package extproto

import (
	"bufio"
	"encoding/json"
)

// MaxFrameBytes is the largest single wire frame the read side accepts.
// A frame over this limit is skipped (logged), never fatal — both the
// host and the SDK read through ReadFrame, which recovers instead of
// dying the way bufio.Scanner does on ErrTooLong.
const MaxFrameBytes = 4 << 20 // 4 MiB

// MaxToolCallBytes caps the serialized args the host will put in one
// tool_call frame sent to an extension. Kept comfortably below
// MaxFrameBytes so the extension can always read what the host sends;
// oversized args come back to the model as a tool error instead of
// killing the extension mid-read.
const MaxToolCallBytes = 1 << 20 // 1 MiB

// ProtocolVersion is the wire revision this host/SDK speaks.
//
//	1 — baseline: tool_result fanout, crash surfacing, min-protocol
//	    negotiation.
//	2 — session identity: session_id / session_path / session_title on
//	    the session_start event, re-fired on every session switch, and
//	    a host guarantee that session_start reaches a subscriber before
//	    that session's first tool invocation (ordered delivery). An
//	    extension that needs per-session state declares RequireProtocol(2).
//	3 — refreshable context + host tool calls. refresh_context lets an
//	    extension replace its static system-prompt block at any time (not
//	    just the register phase), and the host rebuilds the cached system
//	    prompt live; the per-extension static budget is also larger (see
//	    context.go). host_tool_call lets an extension run one of the
//	    HOST's tools (read/bash/an MCP tool…) under the host's own
//	    permission gate and get the result back — the reverse of tool_call
//	    — so a code-execution extension can call host tools from a
//	    sandboxed script. list_sessions / read_session give an extension
//	    read-only, project-scoped access to past session transcripts (for
//	    a session-search index, say). An extension that needs any of these
//	    declares RequireProtocol(3). Additive: a v2 host ignores the new
//	    frames (an unanswered request simply never returns; the static
//	    block stays at its register-phase value).
//
// Not every new message bumps this number. A fire-and-forget event an
// extension *optionally* subscribes to — e.g. transcript_compacted, fired
// after the host compacts the transcript so a memory extension can
// re-snapshot its refreshable context — is purely additive and gracefully
// degrading: an older host records the subscription and simply never
// emits it. Such events need no version bump and no RequireProtocol. Only
// request/response features (whose caller blocks for a host answer) gate
// on the version.
const ProtocolVersion = 3

// Lifecycle event names the host emits to subscribed extensions. These
// constants are the single source of truth: the emit sites use them, the
// hello_ack supported_events advertisement carries KnownEvents, and the
// subscription cap consults IsKnownEvent — so adding an event in one place
// flows everywhere.
const (
	EventSessionStart        = "session_start"
	EventSessionEnd          = "session_end"
	EventTurnStart           = "turn_start"
	EventTurnEnd             = "turn_end"
	EventRunEnd              = "run_end"
	EventToolCall            = "tool_call"
	EventToolResult          = "tool_result"
	EventUserMessage         = "user_message"
	EventAssistantMessage    = "assistant_message"
	EventWorkspaceChanged    = "workspace_changed"
	EventCompactStart        = "compact_start"
	EventTranscriptCompacted = "transcript_compacted"
)

// KnownEvents is the canonical, ordered list of those events. Advertised
// to extensions in hello_ack (supported_events) so an author can discover
// what this host emits without keying off the protocol version.
var KnownEvents = []string{
	EventSessionStart, EventSessionEnd, EventTurnStart, EventTurnEnd, EventRunEnd,
	EventToolCall, EventToolResult, EventUserMessage, EventAssistantMessage,
	EventWorkspaceChanged, EventCompactStart, EventTranscriptCompacted,
}

// FileChange is one entry in a workspace_changed event: a workspace-
// relative, slash-separated path and what happened to it during the turn.
type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // "added" | "modified" | "deleted"
}

var knownEventSet = func() map[string]bool {
	m := make(map[string]bool, len(KnownEvents))
	for _, e := range KnownEvents {
		m[e] = true
	}
	return m
}()

// IsKnownEvent reports whether name is a host-emitted lifecycle event. The
// subscription cap uses it to drop unknown names first; it is NOT used to
// reject a subscription — an extension may optimistically subscribe to a
// newer event an older host doesn't emit (graceful degradation).
func IsKnownEvent(name string) bool { return knownEventSet[name] }

type Frame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type HelloFromExt struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities,omitempty"`
	// MinProtocol is the lowest host ProtocolVersion this extension
	// can run against. Optional and additive: zero (the default, and
	// what every pre-negotiation extension sends) means "no minimum",
	// so old extensions and old hosts interoperate unchanged. When
	// set, a host whose ProtocolVersion is lower refuses to load the
	// extension with a clear message instead of letting it misbehave
	// silently against a wire it doesn't fully speak — the mechanism
	// that lets a terva-only extension fail cleanly on an upstream
	// or an older terva.
	MinProtocol int `json:"min_protocol,omitempty"`
}

type RegisterCommandFromExt struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type RegisterToolFromExt struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	// ReadOnly declares the tool has no side effects (the MCP
	// readOnlyHint analog). Hosts may use it to admit the tool in
	// read-only approval modes; it is additive and optional, so old
	// hosts and extensions interoperate unchanged. An extension that
	// lies here only cheats its own user's policy.
	ReadOnly bool `json:"read_only,omitempty"`
	// Authority is the tool's effect class (core.Authority:
	// local-read / workspace-mutation / process-execution /
	// network-read / external-mutation). It is the richer successor to
	// ReadOnly: a network-read tool reads nothing locally yet must not
	// be auto-allowed as read-only. Additive/optional — empty means the
	// host falls back to the ReadOnly bool. An unknown value is treated
	// as side-effecting (safe default). See docs/standard-tools.md.
	Authority string `json:"authority,omitempty"`
}

type ReadyFromExt struct {
	Type string `json:"type"`
}

// HostToolCallFromExt asks the host to run one of ITS tools (read, bash,
// grep, an MCP tool, …) on the extension's behalf and return the result
// — the reverse of tool_call (where the host drives an extension tool).
// Its purpose is an extension that orchestrates host tools without going
// through the model: e.g. a code-execution extension whose sandboxed
// script calls read/grep/bash as functions, so a multi-step pipeline
// runs in one turn. The host runs the tool under the SAME permission
// policy and approval gate as a model-issued call and may refuse on
// trust grounds (an untrusted project's extension gets no host tools) —
// the extension gains *reach*, never *authority*. ID is the extension's
// correlation id, which the host echoes on the matching
// host_tool_result. Silent asks the host to run the call without
// surfacing it as a model-visible result (the "intermediate calls don't
// pollute context" path). Protocol 3; declare RequireProtocol(3).
type HostToolCallFromExt struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Silent bool            `json:"silent,omitempty"`
}

// HostToolResultFromHost carries a HostToolCallFromExt outcome back to
// the extension, correlated by the extension's ID. Mirrors
// ToolResultFromExt's content/error shape.
type HostToolResultFromHost struct {
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error,omitempty"`
}

// ListSessionsFromExt asks the host for the past sessions of a project
// (protocol 3), so an extension can index them — e.g. a session-search
// store building an FTS index over prior conversations. Read-only and
// project-scoped: the host returns only the active project's sessions
// (ProjectID, if given, must match; cross-project reads are not granted
// here). The host echoes ID on the matching session_list.
type ListSessionsFromExt struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
}

// SessionListFromHost answers ListSessionsFromExt with the project's
// sessions, newest-first as the host stores them.
type SessionListFromHost struct {
	Type     string        `json:"type"`
	ID       string        `json:"id"`
	Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo is one past session's metadata. SessionID is the stable id
// (also what ReadSessionFromExt takes); ModTime is unix seconds.
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	Preview   string `json:"preview,omitempty"`
	Messages  int    `json:"messages,omitempty"`
	ModTime   int64  `json:"mtime,omitempty"`
}

// ReadSessionFromExt asks the host for the transcript of one session by
// id (protocol 3). Read-only; the host serves it only if the session
// belongs to the active project.
type ReadSessionFromExt struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}

// SessionDataFromHost answers ReadSessionFromExt with a flattened
// role+text view of the session's messages — the shape a text index
// wants, not the full tool-call structure. NotFound is set when the id
// is unknown or out of the active project.
type SessionDataFromHost struct {
	Type     string           `json:"type"`
	ID       string           `json:"id"`
	Messages []SessionMessage `json:"messages"`
	NotFound bool             `json:"not_found,omitempty"`
}

// SessionMessage is one transcript entry flattened to role + text.
type SessionMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type SubscribeFromExt struct {
	Type      string   `json:"type"`
	Events    []string `json:"events,omitempty"`
	Intercept []string `json:"intercept,omitempty"`
}

type EventInterceptResponseFromExt struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Block        bool            `json:"block,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	ModifiedArgs json.RawMessage `json:"modified_args,omitempty"`
	ReplaceText  string          `json:"replace_text,omitempty"`
}

type ToolResultFromExt struct {
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error,omitempty"`
}

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type CommandResponseFromExt struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	Action    string     `json:"action"`
	Prompt    string     `json:"prompt,omitempty"`
	Insert    string     `json:"insert,omitempty"`
	Display   string     `json:"display,omitempty"`
	OpenPanel *PanelSpec `json:"open_panel,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type PanelSpec struct {
	ID     string   `json:"id"`
	Title  string   `json:"title,omitempty"`
	Lines  []string `json:"lines,omitempty"`
	Footer string   `json:"footer,omitempty"`
}

// OpenPanelFromExt is a spontaneous one-way frame an extension can send at
// any time to open an interactive panel. Unlike the open_panel action inside
// CommandResponseFromExt, this form is uncoupled from any command invocation
// and may be sent from a tool handler goroutine or any background context.
type OpenPanelFromExt struct {
	Type  string    `json:"type"` // "open_panel"
	Panel PanelSpec `json:"panel"`
}

type PanelRenderFromExt struct {
	Type    string   `json:"type"`
	PanelID string   `json:"panel_id"`
	Title   string   `json:"title,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Footer  string   `json:"footer,omitempty"`
}

type PanelCloseFromExt struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
}

type NotifyFromExt struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// ClearNotesFromExt is a spontaneous frame an extension can send to
// retract every note it previously pushed via notify/display, so
// transient status lines (e.g. an approval prompt) do not stack up
// forever in the bottom-sticky notes block.
type ClearNotesFromExt struct {
	Type string `json:"type"`
}

// SubmitSlashFromExt is a spontaneous frame an extension can send at
// any time (typically from a panel_key handler) to invoke a slash
// command in the host's TUI as if the user had typed it. Text must
// start with '/'. Reserved for internal / opt-in extensions today;
// the wire format is stable but not yet exposed in the public docs.
type SubmitSlashFromExt struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ShutdownAckFromExt struct {
	Type string `json:"type"`
}

// --- Host context contributions (protocol 2, additive) -----------------
//
// These let an extension contribute to what the MODEL sees, under host
// control. The host wraps, bounds, attributes, and places every
// contribution; the extension only supplies content. Two channels with
// different cache/authority profiles:
//
//   - register_context: a STATIC block folded into the cached system
//     prompt addendum (set once, during the register phase). Use it for
//     standing policy/guidance — the text that never changes.
//   - context_card / context_card_clear: a DYNAMIC, per-turn block the
//     host injects at the cache-free tail and never persists. Use it for
//     live state (e.g. a task list) updated as it changes.
//
// status_segment is the adjacent status-bar channel (host-rendered, not
// model-facing). All are additive: an older host treats them as unknown
// frames and ignores them.

// RegisterContextFromExt declares static guidance the host appends to
// the system-prompt addendum (host-wrapped + bounded). Sent during the
// register phase, like register_command / register_tool.
type RegisterContextFromExt struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// RefreshContextFromExt replaces the extension's static system-prompt
// block at any time after the register phase (ProtocolVersion 3+). It is
// register_context you can send mid-session: the host swaps the block,
// rebuilds the cached system prompt, and the change takes effect on the
// next turn. The block stays a *snapshot* — it changes only when the
// extension sends this frame, not per turn — so an extension can
// re-snapshot at a session boundary (new project → that project's notes)
// without churning the prompt cache every turn. Same host wrapping,
// attribution, and byte bound as register_context. Empty Text clears the
// block.
type RefreshContextFromExt struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ContextCardFromExt sets (or replaces, by ID) a dynamic context card
// the host injects into the model's context each turn at the cache-free
// tail. Label is an optional short header for the host's wrapper;
// Priority orders multiple cards (lower first).
type ContextCardFromExt struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Text     string `json:"text"`
	Priority int    `json:"priority,omitempty"`
	// Blocking marks work that should be reviewed before the model
	// declares the task done. The host appends a recommendation to the
	// card's injection (a soft nudge, not a hard stop) so the model sees
	// "review open work before closing" whenever this card is present.
	Blocking bool `json:"blocking,omitempty"`
}

// ContextCardClearFromExt removes a previously-set card by ID.
type ContextCardClearFromExt struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// StatusSegmentFromExt sets (or replaces, by ID) a short status-bar
// segment. Host-rendered in the TUI status line; not model-facing. An
// empty Text clears the segment.
type StatusSegmentFromExt struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

// HelloAckFromHost carries the host's identity. ZotVersion and
// TervaVersion are the SAME value under both naming eras: existing
// extensions parse zot_version, so it is kept indefinitely —
// extension wire compatibility is an explicit invariant of the
// rename (docs/plans/rename-terva.md); the golden tests in
// extproto_test.go enforce it.
type HelloAckFromHost struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	ZotVersion      string `json:"zot_version"`
	TervaVersion    string `json:"terva_version,omitempty"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	CWD             string `json:"cwd"`
	ExtensionDir    string `json:"extension_dir,omitempty"`
	DataDir         string `json:"data_dir,omitempty"`
	// SupportedEvents lists the lifecycle events this host can emit
	// (extproto.KnownEvents). An extension reads it to discover what's
	// available — finer-grained than the protocol version — and can adapt
	// or warn. Absent (older host) means "unknown": the extension should
	// just subscribe optimistically and degrade if the event never fires.
	SupportedEvents []string `json:"supported_events,omitempty"`
}

type CommandInvokedFromHost struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

type ToolCallFromHost struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type EventFromHost struct {
	Type     string          `json:"type"`
	Event    string          `json:"event"`
	Step     int             `json:"step,omitempty"`
	Stop     string          `json:"stop,omitempty"`
	Error    string          `json:"error,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	ToolArgs json.RawMessage `json:"tool_args,omitempty"`
	Text     string          `json:"text,omitempty"`
	// IsError is set on a "tool_result" event when the tool returned
	// an error result. The result's text rides in Text. Additive:
	// pre-fanout hosts never emit tool_result events at all, so older
	// subscribers simply never see this field.
	IsError bool `json:"is_error,omitempty"`
	// SessionID / SessionPath / SessionTitle identify the active
	// session on a "session_start" event (ProtocolVersion 2+). The
	// host fires session_start AFTER the session opens and re-fires it
	// on every switch (/sessions resume, fork, /new). An empty
	// SessionID means there is no active session (e.g. --no-session, or
	// a session was just closed). SessionTitle is presentation-only.
	// Additive/omitempty — pre-v2 subscribers ignore the fields, and a
	// session_start that carries no session info (older emitters)
	// simply leaves them empty.
	SessionID    string `json:"session_id,omitempty"`
	SessionPath  string `json:"session_path,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
	// CWD / ProjectID ride a "session_start" event (ProtocolVersion 2+).
	// Unlike HelloAck's CWD (frozen at the handshake), these refresh on
	// every session_start — including after a /cd — so an extension can
	// follow the working directory instead of going stale on the launch
	// cwd. ProjectID is the host's stable, collision-proof key for the
	// cwd (core.ProjectKey): an extension uses it to scope per-project
	// state without reimplementing the keying. Additive/omitempty —
	// pre-v2 subscribers ignore them, and a no-session start leaves them
	// empty.
	CWD       string `json:"cwd,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	// Files carries the per-turn workspace diff on a "workspace_changed"
	// event (added/modified/deleted, workspace-relative paths). Empty on
	// every other event. Additive/omitempty — older subscribers ignore it.
	Files []FileChange `json:"files,omitempty"`
}

type EventInterceptFromHost struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Event    string          `json:"event"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	ToolArgs json.RawMessage `json:"tool_args,omitempty"`
	Step     int             `json:"step,omitempty"`
	Text     string          `json:"text,omitempty"`
}

type PanelKeyFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
	Key     string `json:"key"`
	Text    string `json:"text,omitempty"`
}

type PanelResizeFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type PanelCloseFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
}

type ShutdownFromHost struct {
	Type string `json:"type"`
}

func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ReadFrame reads one '\n'-terminated frame from r, bounding its length
// at MaxFrameBytes. It is the read counterpart to Encode and the
// recoverable replacement for bufio.Scanner on this wire: where Scanner
// is permanently done after a token exceeds its buffer (ErrTooLong),
// ReadFrame fully consumes an over-limit line through its newline and
// returns tooLong=true with a nil line, so the caller can log it, skip
// it, and keep reading subsequent frames. The returned line excludes
// the trailing newline and is a fresh copy (safe to retain). err is
// io.EOF (or another read error) only at the actual end of the stream;
// a normal frame returns err == nil.
func ReadFrame(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		// The terminating newline doesn't count toward the limit.
		add := len(chunk)
		if e == nil && add > 0 && chunk[add-1] == '\n' {
			add--
		}
		if tooLong {
			// Already over the limit: keep draining to the newline,
			// discarding, so the next frame starts clean.
		} else if len(line)+add > MaxFrameBytes {
			tooLong = true
			line = nil
		} else {
			// ReadSlice's buffer is reused on the next read, so copy now.
			line = append(line, chunk...)
		}
		switch e {
		case nil:
			if !tooLong && len(line) > 0 && line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			return line, tooLong, nil
		case bufio.ErrBufferFull:
			continue // line longer than the bufio buffer; keep reading
		default:
			return line, tooLong, e // io.EOF or a real read error
		}
	}
}
