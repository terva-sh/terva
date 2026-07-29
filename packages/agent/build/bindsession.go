package build

import (
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
)

// SessionBinding is the session-binding event: everything that has to learn
// WHICH session an agent is now running, and — in BindSession — the order it
// learns in.
//
// The fields ARE the list, as in TrustSurfaces and ModelSwap. This one was
// already half-written down: RebindTasks' doc says "Call it wherever
// EmitSessionStart fires", a rule stated in a comment and enforced by nothing,
// which had duly come apart. rpc announced the session and never rebound the
// board; ACP did neither. Both carried a board, and a board that is never
// rebound is never written at all.
//
// It fires on more than an open: a session SWITCH (resume, fork, /new, /cd) is
// the same event with a different session, and a close is the same event with
// none — a nil Session unbinds the board, clears the agent's identity, and
// announces "no active session", which EmitSessionStart's bookend turns into a
// session_end for the outgoing one.
type SessionBinding struct {
	// Agent adopts the session's identity, which is what terva_status reports
	// and what routes the prompt cache. Nil-safe. WireHeadlessSessionPersist
	// also adopts, as part of wiring persistence; adopting twice is idempotent
	// and the two callers are answering different questions.
	Agent *core.Agent

	// Tasks is the built-in board, re-keyed to the session. This is the ONLY
	// thing that keys it: without it the store has no file name and silently
	// declines to persist.
	Tasks *tasktool.Controller

	// Ext receives session_start, so session-aware extensions can key their own
	// per-session state. Only extensions that subscribed to the event by name
	// receive it (extdriver.EmitEvent filters by subscription), so a host
	// supplying a manager is not imposing anything on extensions that did not
	// ask.
	Ext *extensions.Manager

	// Session is the session now in force, or nil for none.
	Session *core.Session
}

// BindSession points one host's session-keyed surfaces at Session.
//
// The order is a rule, not a style. The board is LOADED before the session is
// ANNOUNCED, because announcing is what gives an extension the cue to act: a
// subscriber that answers session_start by calling a task tool must find a
// board that is already loaded and persisting, not one still keyed to nothing.
// EmitEvent's FIFO delivery guarantees the extension sees session_start before
// that session's first tool_call, which is only worth anything if the host has
// finished binding by the time it emits.
//
// Three hosts did both halves and two of them had this backwards.
func BindSession(b SessionBinding) {
	// 1. Who the agent thinks it is.
	if b.Agent != nil {
		b.Agent.AdoptSessionIdentity(b.Session)
	}
	// 2. The board, loaded from the incoming session's file.
	RebindTasks(b.Tasks, b.Session)
	// 3. The announcement, last — see above.
	EmitSessionStart(b.Ext, b.Session)
}
