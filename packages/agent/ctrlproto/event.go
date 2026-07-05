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
)

// Notice is a transient host-originated message shown in the conversation area
// without joining the transcript. Level is "info" or "error"; Ext, when set,
// attributes it to the extension that produced it.
type Notice struct {
	Level string `json:"level"` // info | error
	Text  string `json:"text"`
	Ext   string `json:"ext,omitempty"`
}

// Snapshot is a session's current state, delivered as the first event on a new
// subscription so the client can render existing history atomically before the
// live event stream begins.
type Snapshot struct {
	Session  SessionInfo        `json:"session"`
	Messages []core.WireMessage `json:"messages"`
	Busy     bool               `json:"busy"` // a turn is currently running
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
