package replay

import "terva.sh/terva/packages/core"

// The four events below exist for a replay and live here rather than in
// package core, whose gate requires every declared event to be one core
// emits. A live session never emits these: its permission prompts and
// questions travel as ctrlproto events the workspace broadcasts, outside the
// AgentEvent stream. The synth has only that stream to speak in, so it emits
// these, and the carrier turns them into the same wire events a live client
// already renders. They satisfy core.AgentEvent so a Frame can carry them.

// EvPermissionRequest opens a tool-approval prompt in a replay.
type EvPermissionRequest struct {
	CallID  string
	Tool    string
	Preview string
}

func (EvPermissionRequest) Type() string { return "permission_request" }

// EvPermissionResolved closes it with the decision that was taken.
type EvPermissionResolved struct {
	CallID string
	Allow  bool
	Reason string
	Scope  string // a core.PermissionScope* value
}

func (EvPermissionResolved) Type() string { return "permission_resolved" }

// EvAskRequest opens a question set in a replay.
type EvAskRequest struct {
	AskID     string
	Questions []core.UserQuestion
}

func (EvAskRequest) Type() string { return "ask_request" }

// EvAskResolved closes it with the answers that were given.
type EvAskResolved struct {
	AskID   string
	Answers []core.UserAnswer
}

func (EvAskResolved) Type() string { return "ask_resolved" }
