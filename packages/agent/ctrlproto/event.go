package ctrlproto

import "terva.sh/terva/packages/core"

// Event is one item on a session's stream. It embeds core's canonical
// [core.WireEvent] verbatim — so every conversation event (text deltas, tool
// calls, results, usage, turn boundaries, errors) is exactly the shape every
// other terva surface already emits — and adds only the control-plane events a
// browser must render and answer.
//
// A conversation event has its WireEvent fields set and the control pointers
// nil, so its JSON is byte-identical to a bare WireEvent. A control event sets
// Type to one of the Event* constants and populates the matching pointer.
type Event struct {
	core.WireEvent
	// Permission is set on an [EventPermissionRequest] event.
	Permission *PermissionRequest `json:"permission,omitempty"`
	// Ask is set on an [EventAskRequest] event.
	Ask *AskRequest `json:"ask,omitempty"`
	// Resolved is set on an [EventPermissionResolved] / [EventAskResolved]
	// event, telling other clients a request they may be showing is now
	// answered and their dialog can be dismissed.
	Resolved *Resolved `json:"resolved,omitempty"`
	// Snapshot is set on an [EventSnapshot] event: the session's current
	// transcript, sent to a client the moment it subscribes so it can render
	// history before the live stream begins.
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	// Info is set on an [EventSessionUpdated] event: a session's metadata (its
	// title, model, cost) changed and every client should refresh its header and
	// session list without re-fetching.
	Info *SessionInfo `json:"info,omitempty"`
	// Queued is set on an [EventQueueUpdated] event: the session's full pending
	// message queue after a change, so every client's queued-message view
	// converges. A present-but-empty slice means the queue was cleared.
	Queued []string `json:"queued,omitempty"`
	// SurfaceID is set on an [EventSurfaceUpdated] event: the id of the pane
	// whose content changed. The event is a signal — a client showing that
	// surface re-fetches it with surface.get.
	SurfaceID string `json:"surface_id,omitempty"`
	// Locale is set on an [EventLocaleChanged] event: the daemon's new active
	// language. Every client re-fetches its string catalog and re-renders.
	Locale string `json:"locale,omitempty"`
	// Notice is set on an [EventNotice] event: a one-shot host-originated message
	// to show in the conversation area without adding it to the transcript.
	Notice *Notice `json:"notice,omitempty"`
	// Replay is set on an [EventReplayState] event: a replay session's transport
	// changed (play/pause/seek/speed), so every client's scrubber converges.
	Replay *ReplayState `json:"replay,omitempty"`
	// Auth is set on an [EventAuthState] event: a model-provider login flow moved.
	// Broadcast on the WORKSPACE address — a credential belongs to the daemon, not
	// to any session, and the client that started the login may not have one in
	// focus when it lands.
	Auth *AuthState `json:"auth,omitempty"`
}

// Control-plane event types. These extend the [core.WireEvent] type space; the
// conversation event types themselves live in package core.
const (
	// EventPermissionRequest asks connected clients to approve a tool call.
	// Broadcast to every subscriber; the first to [WorkspaceService.Approve]
	// resolves it.
	EventPermissionRequest = "permission_request"
	// EventAskRequest asks connected clients a mid-turn question. First
	// [WorkspaceService.Answer] resolves it.
	EventAskRequest = "ask_request"
	// EventPermissionResolved tells clients a permission request was answered
	// (by another client, a remembered decision, or cancellation).
	EventPermissionResolved = "permission_resolved"
	// EventAskResolved tells clients a question was answered.
	EventAskResolved = "ask_resolved"
	// EventSnapshot carries a session's current transcript to a client that
	// just subscribed, so it can render history before the live stream. Sent
	// only to the new subscriber, not broadcast.
	EventSnapshot = "snapshot"
	// EventSessionUpdated announces that a session's metadata changed — a title
	// settled after the first exchange, a rename, a model switch — so every
	// connected client updates its header and session list live.
	EventSessionUpdated = "session_updated"
	// EventQueueUpdated announces the session's new pending message queue after
	// an enqueue or an edit/cancel, so every client's queued view converges.
	EventQueueUpdated = "queue_updated"
	// EventSurfaceUpdated signals that a pane's content changed (SurfaceID); a
	// client showing it re-fetches with surface.get.
	EventSurfaceUpdated = "surface_updated"
	// EventSurfacesChanged signals that the set of available panes changed (an
	// extension panel opened/closed, status appeared); the client re-lists.
	EventSurfacesChanged = "surfaces_changed"
	// EventSessionsChanged signals that the SET of sessions changed shape —
	// create, delete, rename, or a cold session materializing live. It carries
	// no payload: a client answers it by re-running sessions.list, which is the
	// single source of truth (a delta in the event would invent a second). Like
	// surfaces_changed it is workspace-scoped, broadcast on [AddrWorkspace] via
	// Workspace.BroadcastAll; a board uses it to auto-populate and prune tiles
	// without polling. Additive: an old client that never negotiated it simply
	// ignores the unknown type.
	EventSessionsChanged = "sessions_changed"
	// EventLocaleChanged announces that the daemon's active UI language changed
	// (Locale). Every client re-fetches its string catalog (i18n.catalog) and
	// re-renders; a client showing panes also re-lists/re-fetches them, since
	// server-rendered titles/labels are now in the new language.
	EventLocaleChanged = "locale_changed"
	// EventNotice carries a one-shot host-originated message (Notice) to show in
	// the conversation area — e.g. the display/error result of an extension
	// command run from the commands pane. Ephemeral: not persisted to the
	// transcript, not replayed in a snapshot.
	EventNotice = "notice"
	// EventReplayState carries a replay session's transport state (Replay) after
	// a play/pause/seek/speed change, so every client's scrubber stays in sync.
	EventReplayState = "replay_state"
	// EventAuthState carries a model-provider login flow's progress (Auth):
	// started, succeeded, failed, cancelled. Broadcast on [AddrWorkspace].
	//
	// It exists because a login is asynchronous and finishes ELSEWHERE — you
	// authorize in a browser on another device, and the daemon finds out by
	// polling. The client that started the flow cannot know when it landed unless
	// it is told, and polling auth.providers for it would mean a panel that
	// updates on the next tick rather than the moment you finish.
	EventAuthState = "auth_state"
)

// Notice is a transient host-originated message shown in the conversation area
// without joining the transcript. Level is "info" or "error"; Ext, when set,
// attributes it to the extension that produced it.
//
// Kind, when set, is the machine-readable notice type (one of the Notice*
// kind constants below) with its structured payload in Data. Text always
// carries a self-sufficient human rendering, so a client that doesn't know a
// kind just shows the text — while a kind-aware client can filter, route, or
// re-render: a single-user TUI shows a prompt-rebuild inline, a fleet control
// plane might aggregate the same notices across a hundred daemons instead.
// New kinds are additive protocol surface: document the kind's Data keys on
// its constant.
type Notice struct {
	Level string `json:"level"` // info | error
	Text  string `json:"text"`
	Ext   string `json:"ext,omitempty"`
	Kind  string `json:"kind,omitempty"`
	// Data is the kind's structured payload (keys documented per kind).
	// String-valued deliberately: notices are transient hints, not state —
	// anything a client must track lives on a real event/surface instead.
	Data map[string]string `json:"data,omitempty"`
}

// Notice kinds. A kind names a recurring, machine-actionable situation; plain
// one-off messages leave Kind empty.
const (
	// NoticePromptRebuilt: the session's pinned prompt prefix — the system
	// prompt and/or the model-facing tool set — changed, so the provider's
	// prompt cache is invalidated and the next turn re-reads the transcript
	// uncached. Emitted only on a real diff (an identical rebuild is silent).
	// Data keys: "scope" (system | tools | both), "reason" (approval-mode |
	// auto-swarm | extension-reload | mcp-toggle | trust | tool-withdrawal |
	// extension-context | extensions-ready | chat-connect | chat-disconnect), and "context_tokens" (the approximate token count the
	// next turn re-reads, from the last turn's usage; omitted when no turn has
	// run yet). The extension-driven reasons (tool-withdrawal,
	// extension-context, extensions-ready) are suppressed to a host log when
	// they fire before the first turn — a startup policy assertion, or the
	// background extension start landing its tools, invalidates no cache.
	NoticePromptRebuilt = "prompt_rebuilt"

	// NoticeRestart: a daemon restart lifecycle event — one kind for the whole
	// arc so a client (or a fleet control plane) can render "restart in progress"
	// rather than a generic message, and distinguish it from a crash. Data keys:
	// "phase" (restarting | failed | recovered), "from_version" (the outgoing
	// build, when known), "to_version" (the running build; recovered only), and
	// "session" (the session that resumed; recovered only). The text is always a
	// self-sufficient human rendering, so a kind-unaware client still shows it.
	NoticeRestart = "restart"
)

// Snapshot is a session's current state, delivered as the first event on a new
// subscription so the client can render existing history atomically before the
// live event stream begins.
type Snapshot struct {
	Session  SessionInfo        `json:"session"`
	Messages []core.WireMessage `json:"messages"`
	// Epoch, Base and Total place Messages within the session's transcript.
	//
	// Epoch is the agent's transcriptEpoch, which increments ONLY when the
	// transcript is wholesale replaced or shrunk (compact, /clear, SetMessages) and
	// never on a plain append. So within one epoch the transcript only grows and an
	// index never moves, which makes (Epoch, index) a stable identity for a message
	// — the thing a client needs to MERGE a snapshot instead of replacing its whole
	// list. An epoch a client has not seen means the transcript changed under it,
	// which is exactly when a full rebuild is the correct answer rather than a
	// fallback.
	//
	// Base is the index of Messages[0] in the full transcript and Total its true
	// length. For a client that negotiated FeatureHistoryWindow, Messages is a TAIL
	// WINDOW and Base > 0 means there is more above it (fetch with
	// conversation.history). For every other client Messages is the whole transcript
	// and Base is 0, as it has always been.
	Epoch uint64 `json:"epoch,omitempty"`
	Base  int    `json:"base,omitempty"`
	Total int    `json:"total,omitempty"`
	Busy  bool   `json:"busy"` // a turn is currently running
	// Permissions / Asks are requests still awaiting an answer, so a client that
	// (re)subscribes mid-turn — e.g. after a suspended tab reconnects — restores
	// the dialog instead of leaving the parked turn invisible.
	Permissions []PermissionRequest `json:"permissions,omitempty"`
	Asks        []AskRequest        `json:"asks,omitempty"`
	// Queued is the pending message queue, so a reconnecting client restores the
	// queued-message view.
	Queued []string `json:"queued,omitempty"`
	// Skills are the session's available skills, so the client can autocomplete
	// the `/skill` composer command by name.
	Skills []SkillInfo `json:"skills,omitempty"`
	// Tail describes the tail span's swipe alternatives when the last response
	// has more than one (a regenerated/reloaded turn, or pre-seeded greetings).
	// Absent when there is nothing to swipe. A client draws the swipe arrows from
	// Variants/Active and switches with turn.swipe{variant}.
	Tail *TailInfo `json:"tail,omitempty"`
	// VariantMarks is the superset of Tail: EVERY switchable position — the tail
	// span plus message-scoped edit variants at any position (Option C). Each mark
	// draws a swipe control at Index. A client that reads VariantMarks should prefer
	// it over Tail (it includes the tail, with Span=true) and switch with
	// turn.swipe{index, variant}; Tail remains for clients that only draw the tail.
	VariantMarks []VariantMark `json:"variant_marks,omitempty"`
}

// VariantMark places one switchable position for the per-position swipe UI: its
// transcript Index, how many takes it has, which is active, and whether it is the
// tail suffix span (Span=true — the whole last response) or a message-scoped edit
// (Span=false — one message with shared downstream).
type VariantMark struct {
	Index    int  `json:"index"`
	Variants int  `json:"variants"`
	Active   int  `json:"active"`
	Span     bool `json:"span,omitempty"`
}

// TailInfo is the swipe state of the transcript's tail span (see [Snapshot.Tail]):
// the span the swipe UI acts on, how many alternatives it has, and which is live.
type TailInfo struct {
	SpanStart int `json:"span_start"` // effective index where the swipeable span begins
	Variants  int `json:"variants"`   // number of alternative takes (>= 2)
	Active    int `json:"active"`     // index of the take currently in the transcript
}

// SkillInfo is one available skill, surfaced for `/skill` autocomplete.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PermissionRequest is a pending tool-approval round-trip. The client renders
// it and replies with [WorkspaceService.Approve] using CallID.
type PermissionRequest struct {
	CallID  string `json:"call_id"`
	Tool    string `json:"tool"`
	Preview string `json:"preview,omitempty"`
	// Agent, when set, is the id of the swarm worker whose tool call this
	// request is for: the daemon routes a foreign worker's ask onto the
	// dispatching session's card (see workspace.workerConfirmer), and this is
	// the machine-readable link back to the worker so a board can badge the
	// right lane tile without parsing the CallID convention. Empty for the
	// session's OWN tool calls; omitempty keeps it off the wire for ordinary
	// approvals and for old daemons that never set it.
	Agent string `json:"agent,omitempty"`
	// Scopes are the derived narrow-grant options for this call (bash
	// only today): a dialog may offer "always allow <Display>" and echo
	// the accepted Patterns back in Decision.PersistScopes. Derived
	// daemon-side (the preview is truncated and clients must never parse
	// it); empty for underivable commands and non-bash tools, and off
	// the wire entirely for old daemons.
	Scopes []GrantScope `json:"scopes,omitempty"`
}

// GrantScope is the wire form of [core.GrantScope]: one derived
// narrow-grant option. Display is what the dialog shows ("git status"),
// Pattern the RE2 a scoped grant persists.
type GrantScope struct {
	Display string `json:"display"`
	Pattern string `json:"pattern"`
}

// AskRequest is a pending mid-turn question (the ask_user_question tool). The
// client renders it and replies with [WorkspaceService.Answer] using AskID.
type AskRequest struct {
	AskID       string   `json:"ask_id"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
}

// Resolved identifies which pending request an [EventPermissionResolved] /
// [EventAskResolved] event refers to. Exactly one id field is set.
type Resolved struct {
	CallID string `json:"call_id,omitempty"`
	AskID  string `json:"ask_id,omitempty"`
}

// ConversationEvent wraps a core wire event as a ctrlproto Event.
func ConversationEvent(w core.WireEvent) Event { return Event{WireEvent: w} }

// PermissionEvent builds an [EventPermissionRequest] event.
func PermissionEvent(r PermissionRequest) Event {
	return Event{WireEvent: core.WireEvent{Type: EventPermissionRequest}, Permission: &r}
}

// AskEvent builds an [EventAskRequest] event.
func AskEvent(r AskRequest) Event {
	return Event{WireEvent: core.WireEvent{Type: EventAskRequest}, Ask: &r}
}

// PermissionResolvedEvent builds an [EventPermissionResolved] event.
func PermissionResolvedEvent(callID string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventPermissionResolved}, Resolved: &Resolved{CallID: callID}}
}

// AskResolvedEvent builds an [EventAskResolved] event.
func AskResolvedEvent(askID string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventAskResolved}, Resolved: &Resolved{AskID: askID}}
}

// SnapshotEvent builds an [EventSnapshot] event.
func SnapshotEvent(s Snapshot) Event {
	return Event{WireEvent: core.WireEvent{Type: EventSnapshot}, Snapshot: &s}
}

// ReplayStateEvent builds an [EventReplayState] event carrying a replay
// session's fresh transport state.
func ReplayStateEvent(s ReplayState) Event {
	return Event{WireEvent: core.WireEvent{Type: EventReplayState}, Replay: &s}
}

// SessionUpdatedEvent builds an [EventSessionUpdated] event carrying the
// session's fresh metadata.
func SessionUpdatedEvent(info SessionInfo) Event {
	return Event{WireEvent: core.WireEvent{Type: EventSessionUpdated}, Info: &info}
}

// QueueUpdatedEvent builds an [EventQueueUpdated] event. An empty queue
// serializes as an absent Queued field (omitempty); the event type is the
// signal, so a client reads Queued as "the whole list, empty if absent".
func QueueUpdatedEvent(texts []string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventQueueUpdated}, Queued: texts}
}

// SurfaceUpdatedEvent builds an [EventSurfaceUpdated] signal for one pane.
func SurfaceUpdatedEvent(surfaceID string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventSurfaceUpdated}, SurfaceID: surfaceID}
}

// SurfacesChangedEvent builds an [EventSurfacesChanged] signal (re-list panes).
func SurfacesChangedEvent() Event {
	return Event{WireEvent: core.WireEvent{Type: EventSurfacesChanged}}
}

// SessionsChangedEvent builds an [EventSessionsChanged] signal (re-list
// sessions). No payload: the client re-runs sessions.list.
func SessionsChangedEvent() Event {
	return Event{WireEvent: core.WireEvent{Type: EventSessionsChanged}}
}

// LocaleChangedEvent builds an [EventLocaleChanged] signal carrying the new
// active language, broadcast to every session when the operator switches it.
func LocaleChangedEvent(locale string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventLocaleChanged}, Locale: locale}
}

// NoticeEvent builds an [EventNotice] event carrying a one-shot message (e.g. an
// extension command's display/error result) for the conversation area.
func NoticeEvent(level, ext, text string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventNotice}, Notice: &Notice{Level: level, Ext: ext, Text: text}}
}

// KindedNoticeEvent builds an [EventNotice] event with a machine-readable Kind
// and its structured Data payload. Text must still stand alone — it is what a
// kind-unaware client renders.
func KindedNoticeEvent(level, kind, text string, data map[string]string) Event {
	return Event{WireEvent: core.WireEvent{Type: EventNotice}, Notice: &Notice{Level: level, Kind: kind, Text: text, Data: data}}
}
