// Package extproto defines the JSON-over-stdin/stdout wire format
// spoken between terva and its extension subprocesses. Both the host
// (packages/agent/extensions) and the SDK (packages/agent/ext) marshal/
// unmarshal the same types, so changes here ripple through both.
//
// All frames are one JSON object terminated by a single LF. Object
// boundaries follow newline boundaries; no multi-line JSON.
//
// Direction conventions in this file:
//   - Type names ending in "FromExt" are sent by the extension to terva.
//   - Type names ending in "FromHost" are sent by terva to the extension.
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

	"terva.sh/terva/packages/lineframe"
)

// MaxFrameBytes is the largest single wire frame the read side accepts.
// A frame over this limit is skipped (logged), never fatal — both the
// host and the SDK read through ReadFrame, which recovers via lineframe
// instead of dying the way bufio.Scanner does on ErrTooLong.
const MaxFrameBytes = lineframe.DefaultMaxBytes

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
//	4 — per-session tool withdrawal. set_withdrawn_tools lets an extension
//	    hide (and later restore) its OWN already-registered tools for the
//	    session, so a git extension outside a repo — or a cloud extension
//	    with no credentials — stops paying tokens and inviting misfires for
//	    tools that can only refuse. It is the tool analog of refresh_context:
//	    a wholesale withdrawn-set snapshot, host-diffed so an unchanged set
//	    costs nothing, applied at the single ext.tools enumeration so the
//	    tool drops from both the callable registry and the system prompt.
//	    NOTE: the frame is itself fire-and-forget (no blocking reply) and so
//	    by the rule below needs no bump on its own — a protocol-3 host
//	    silently ignores it and the tools simply stay visible (today's
//	    behavior). The bump exists ONLY so an extension can feature-detect
//	    (Host().ProtocolVersion >= 4) and not believe it hid tools an older
//	    host ignored. There is no RequireProtocol(4): older hosts keep
//	    loading the extension.
//	5 — connector role (experimental). register_connector declares that
//	    ONE extension process is also a chat connector, and the chat_open /
//	    chat / chat_close / chat_down envelope frames then TUNNEL the
//	    connector protocol (packages/agent/connproto) through this wire
//	    verbatim — the extension/connector unification
//	    (docs/proposals/connector-extensions.md). This protocol never
//	    mirrors connproto's vocabulary: the inner frames (hello, connect,
//	    message, send, result, …) ride opaquely inside `chat` envelopes,
//	    including connproto's own hello/hello_ack version negotiation, so
//	    connproto can grow — and renegotiate its version — without this
//	    protocol changing at all. Every envelope frame degrades gracefully
//	    on an older host (unknown frames are logged and ignored; the tools
//	    keep working — the connector role just never activates), so by the
//	    rule below the bump exists ONLY for feature detection
//	    (Host().ProtocolVersion >= 5): an extension must not sit waiting
//	    for a chat_open an older host will never send. A connector-only
//	    extension MAY RequireProtocol(5) to refuse loading where its whole
//	    purpose is unavailable.
//
// Not every new message bumps this number. A fire-and-forget event an
// extension *optionally* subscribes to — e.g. transcript_compacted, fired
// after the host compacts the transcript so a memory extension can
// re-snapshot its refreshable context — is purely additive and gracefully
// degrading: an older host records the subscription and simply never
// emits it. Such events need no version bump and no RequireProtocol. Only
// request/response features (whose caller blocks for a host answer) gate
// on the version.
//
//	6 — extension secrets. secret_set / secret_get / secret_delete /
//	    secret_list let an extension keep credentials it acquires at RUNTIME
//	    (an OAuth token it negotiated, an API key a user pasted into its own
//	    UI) without writing them to ext-data/ in the clear, where a model
//	    reading files finds them. The host seals them into its own scoped
//	    store, so unlike a connector an extension needs no key of its own —
//	    it never runs when terva does not, so a key to generate, store, back
//	    up and rotate would buy nothing. Scoping is HOST-enforced: the driver
//	    substitutes the calling extension's manifest name and the frames carry
//	    no scope to forge. These BLOCK on a reply, so an extension that needs
//	    them declares RequireProtocol(6) rather than hanging against a v5
//	    host.
const ProtocolVersion = 6

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
	// EventConfigUpdate fires when the user changes this extension's config
	// (via the /extensions config dialog). The new resolved values ride the
	// event's Config field. Fire-and-forget and gracefully degrading (an
	// older host never emits it), so — like transcript_compacted — it needs
	// no protocol bump; the extension's initial values still arrive in
	// hello_ack regardless.
	EventConfigUpdate = "config_update"
)

// KnownEvents is the canonical, ordered list of those events. Advertised
// to extensions in hello_ack (supported_events) so an author can discover
// what this host emits without keying off the protocol version.
var KnownEvents = []string{
	EventSessionStart, EventSessionEnd, EventTurnStart, EventTurnEnd, EventRunEnd,
	EventToolCall, EventToolResult, EventUserMessage, EventAssistantMessage,
	EventWorkspaceChanged, EventCompactStart, EventTranscriptCompacted,
	EventConfigUpdate,
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
	// Essential marks a load-bearing tool that must stay advertised every
	// turn even under lazy tool visibility, instead of lazy-loading behind
	// an activate_tools call like the rest of its extension's group. Use it
	// for a tool the extension's static guidance (register_context) tells
	// the model to reach for before others — "search the index before you
	// read a file" — so the tool that guidance names is actually in the
	// tools array, not sitting deferred while the prompt points at it. The
	// tool keeps its extension capability group, so its non-essential
	// siblings still lazy-load; this only pins THIS tool's visibility. The
	// host caps how many tools one extension may mark essential (excess are
	// loaded deferred and logged) so an extension can't defeat lazy mode
	// wholesale. Additive/optional: an older host ignores it (the tool
	// lazy-loads as before) and a host with lazy mode off advertises
	// everything regardless, so it is a no-op there — hence no protocol bump.
	Essential bool `json:"essential,omitempty"`
}

type ReadyFromExt struct {
	Type string `json:"type"`
}

// TypeBootstrap names the pre-hello progress frame.
const TypeBootstrap = "bootstrap"

// BootstrapFromExt is sent BEFORE hello, zero or more times, by a launcher
// that has to build or fetch the extension before it can exec the process that
// speaks this protocol:
//
//	{"type":"bootstrap","message":"compiling extension"}
//
// It exists because the hello deadline conflates two questions — "is this
// process alive?" (seconds) and "how long may becoming the real process take?"
// (a cold compile: minutes) — and the extension cannot answer the second,
// since the thing that would answer is the artifact still being built. Each
// frame restarts the hello deadline, so the host measures silence rather than
// total elapsed time, and reports the message so a long build reads as
// progress instead of a hang. An absolute ceiling still applies.
//
// Handled entirely in the host's spawn path, never by the SDK: the SDK isn't
// running yet, which is precisely the situation. A launcher emits it with one
// `printf`. Additive — a host that doesn't know the frame treats it as a
// malformed hello and skips the extension, which is what happens today anyway.
type BootstrapFromExt struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
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

// SecretSetFromExt stores one secret for the calling extension. The frame
// carries no scope: the host substitutes the extension's manifest name, so
// there is nothing here to forge.
type SecretSetFromExt struct {
	Type  string `json:"type"` // "secret_set"
	ID    string `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SecretGetFromExt reads back one secret the extension itself stored.
//
// Deliberately NOT a way to read config secrets: those already arrive opened,
// through hello_ack and PushConfigUpdate, and a third path to one value is how
// they drift. It would also bypass the one thing delivery does that matters —
// an unopenable value is DROPPED, not delivered — so an extension asking
// directly would get an error where delivery gives an absence.
type SecretGetFromExt struct {
	Type string `json:"type"` // "secret_get"
	ID   string `json:"id"`
	Key  string `json:"key"`
}

// SecretDeleteFromExt forgets one secret.
type SecretDeleteFromExt struct {
	Type string `json:"type"` // "secret_delete"
	ID   string `json:"id"`
	Key  string `json:"key"`
}

// SecretListFromExt asks for the extension's own key NAMES.
type SecretListFromExt struct {
	Type string `json:"type"` // "secret_list"
	ID   string `json:"id"`
}

// SecretValueFromHost answers SecretGetFromExt. Found distinguishes "absent"
// from "present and empty" without overloading Value.
type SecretValueFromHost struct {
	Type  string `json:"type"` // "secret_value"
	ID    string `json:"id"`
	Found bool   `json:"found"`
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// SecretKeysFromHost answers SecretListFromExt with key NAMES only. Never
// values: a listing surface that returns values is one mistake away from being
// a listing surface that leaks.
type SecretKeysFromHost struct {
	Type  string   `json:"type"` // "secret_keys"
	ID    string   `json:"id"`
	Keys  []string `json:"keys"`
	Error string   `json:"error,omitempty"`
}

// SecretAckFromHost answers secret_set and secret_delete.
type SecretAckFromHost struct {
	Type  string `json:"type"` // "secret_ack"
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
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
	// Widgets is an optional generic widget tree. A rich frontend (the web
	// control panel) renders it natively; text frontends (the TUI) fall back to
	// Lines, so an extension targeting both sends both. Mapped to the
	// control-plane Widget vocabulary at the web boundary.
	Widgets []Widget `json:"widgets,omitempty"`
}

// Widget is one node of a generic pane tree an extension can send instead of (or
// alongside) flat panel Lines — the extensible content model for rich frontends.
// Type discriminates; the other fields are per-type. The vocabulary is semantic
// (a meter is a bar, not a styled div) so each frontend renders it natively.
type Widget struct {
	Type     string       `json:"type"` // heading|text|meter|keyvalue|table|list|group|note|action|divider
	Text     string       `json:"text,omitempty"`
	Tone     string       `json:"tone,omitempty"` // default|muted|danger|ok|warn
	Level    int          `json:"level,omitempty"`
	Label    string       `json:"label,omitempty"`
	Value    float64      `json:"value,omitempty"`
	Max      float64      `json:"max,omitempty"`
	Unit     string       `json:"unit,omitempty"`
	Rows     []WidgetKV   `json:"rows,omitempty"`
	Columns  []string     `json:"columns,omitempty"`
	Cells    [][]string   `json:"cells,omitempty"`
	Items    []WidgetItem `json:"items,omitempty"`
	Children []Widget     `json:"children,omitempty"`
	ActionID string       `json:"action_id,omitempty"`
}

// WidgetKV is one row of a keyvalue widget.
type WidgetKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
	Mono  bool   `json:"mono,omitempty"`
}

// WidgetItem is one entry of a list widget; ActionID makes it a button that
// invokes a panel key/action back on the extension.
type WidgetItem struct {
	Text     string `json:"text"`
	Note     string `json:"note,omitempty"`
	Tone     string `json:"tone,omitempty"`
	ActionID string `json:"action_id,omitempty"`
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
	Widgets []Widget `json:"widgets,omitempty"`
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

// SubmitFromExt is a spontaneous frame an extension can send at any time
// to queue a plain model prompt in the interactive host. The driver
// routes it through HostHooks.Submit, so the TUI handles busy state and
// turn startup (SubmitOrQueue). Empty/whitespace text is ignored.
type SubmitFromExt struct {
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

// SetWithdrawnToolsFromExt is the tool analog of refresh_context (protocol
// 4): a wholesale snapshot of THIS extension's tools to hide from the model
// for the session. Wholesale-replace, so the single payload doubles as the
// restore path and gives the host idempotency for free. Fire-and-forget.
//
//	All:true              hide every tool this extension registered (Tools ignored).
//	All:false, len>0      hide exactly the named tools that this extension owns.
//	All:false, Tools:[]   the empty set — restore everything to visible.
//
// Names that aren't this extension's own registered tools are ignored: an
// extension can never hide a built-in or another extension's tool. A
// protocol-3 host ignores the unknown frame, so the tools stay visible.
type SetWithdrawnToolsFromExt struct {
	Type  string   `json:"type"`  // "set_withdrawn_tools"
	Tools []string `json:"tools"` // this ext's tool names to hide; ignored when All
	All   bool     `json:"all"`   // hide every tool this extension registered
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

// --- Connector role (protocol 5, additive, experimental) ---------------
//
// These frames let ONE extension process also be a chat connector — the
// extension/connector unification (docs/proposals/connector-extensions.md).
// The extension declares the role with register_connector during the
// register phase; the host REFUSES the role unless the extension's
// manifest also declares "connector": true, so an installed tool
// extension can never quietly grow into a message source.
//
// The chat session itself is NOT expressed in this protocol. When a chat
// consumer selects the extension (`terva bot run --connector <name>`, the
// TUI's /connect), the host opens a tunnel (chat_open) and the two sides
// run the CONNECTOR protocol (packages/agent/connproto) through it: each
// `chat` envelope carries one connproto frame verbatim, starting with the
// extension's hello and the host's hello_ack — so connproto's own
// negotiated versioning applies inside the tunnel, and connproto can grow
// new frames or bump its version without this protocol changing. Exactly
// one implementation of the connector wire exists on each side
// (host: packages/agent/chat/connhost; author: connsdk.Serve); this
// envelope only moves its bytes.
//
// Envelope frames carry a host-minted session ID so a close/reopen can
// never interleave: frames for a stale session are dropped by both sides.
// Sessions are serial — one live session per extension.

// RegisterConnectorFromExt declares this extension is also a chat
// connector (register phase, alongside register_tool). Refused — logged
// and ignored — when the manifest lacks "connector": true. Carries no
// payload: capabilities, identity, and protocol range all travel in the
// inner connproto hello at chat_open time.
type RegisterConnectorFromExt struct {
	Type string `json:"type"` // "register_connector"
}

// ChatOpenFromHost opens a chat tunnel session. The extension answers by
// starting its connector engine, whose first output is the inner
// connproto hello (wrapped in a `chat` envelope). Sent when a chat
// consumer selects this extension, never as a side effect of loading.
type ChatOpenFromHost struct {
	Type string `json:"type"` // "chat_open"
	ID   string `json:"id"`   // session id; echoed on every session frame
}

// ChatFrame carries one connproto frame verbatim, either direction.
// Frame is opaque to this protocol: neither the extension driver nor the
// SDK run loop parses it — the connproto endpoints on each side do.
type ChatFrame struct {
	Type  string          `json:"type"` // "chat"
	ID    string          `json:"id"`   // session id from chat_open
	Frame json.RawMessage `json:"frame"`
}

// ChatCloseFromHost ends the session from the host side (the chat
// consumer left: daemon shutdown, /connect disconnect). The extension
// tears down its connector engine — the transport disconnects from its
// service — and confirms with chat_down; the PROCESS stays alive and its
// tools keep working (unlike connproto's shutdown, which ends the
// process). A later chat_open starts a fresh session.
type ChatCloseFromHost struct {
	Type string `json:"type"` // "chat_close"
	ID   string `json:"id"`   // session id from chat_open
}

// ChatDownFromExt ends the session from the extension side: the
// connector engine exited. With Error set, the engine died (auth revoked
// mid-run, unrecoverable gateway loss) and the host ends the chat
// consumer with that reason — the connproto analog is process exit +
// restart budget, but a dual-role process must not die (or be blindly
// restarted, which would desync the tool registry) just because its
// connector half failed, so the failure gets a frame and the tools keep
// working. Without Error it confirms an orderly teardown (chat_close).
type ChatDownFromExt struct {
	Type  string `json:"type"` // "chat_down"
	ID    string `json:"id"`   // session id from chat_open
	Error string `json:"error,omitempty"`
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
	// Config carries the resolved values for THIS extension (the manifest's
	// declared defaults overlaid with what the user saved under config.json
	// `extensions.<name>`), keyed by config field key. Absent/empty when the
	// extension declares no config or the user set nothing. Live changes
	// arrive afterward on a config_update event. Additive — older SDKs
	// ignore it. Secret values are PLAINTEXT here (user-scoped config),
	// never logged by the host.
	Config map[string]json.RawMessage `json:"config,omitempty"`
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
	// Config carries this extension's new resolved values on a
	// "config_update" event (same shape as HelloAck.Config). Empty on every
	// other event. Additive/omitempty — older subscribers ignore it.
	Config map[string]json.RawMessage `json:"config,omitempty"`
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

// PanelResizeFromHost would tell an extension its panel's drawing area
// changed, so a renderer could re-wrap to the new width/height.
//
// SPEC-ONLY, and always has been: nothing in this tree emits it. There is
// no producer, no Driver.SendPanelResize beside SendPanelKey/SendPanelClose,
// and no SDK handler beside OnPanelKey — panels re-render on the
// extension's own panel_render cadence. The type and its documentation
// have coexisted without an implementation since the original package
// split, and docs/extensions.md says so to extension authors outright.
//
// It stays because the wire spec is published (the golden corpus pins its
// bytes, so an SDK decoding it defensively gets them right) and removing a
// frame from a published spec costs more than carrying it. Adding a
// producer is a behaviour change that reverses a stated decision — not a
// loose end to tidy.
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

// ReadFrame reads one '\n'-terminated frame from r, bounding its length at
// MaxFrameBytes. It is the read counterpart to Encode; a thin wrapper over
// lineframe.ReadFrame, the recoverable framing contract shared across every
// terva wire protocol (an over-limit line is drained and flagged tooLong
// instead of poisoning the stream the way bufio.Scanner's ErrTooLong does).
func ReadFrame(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	return lineframe.ReadFrame(r, MaxFrameBytes)
}
