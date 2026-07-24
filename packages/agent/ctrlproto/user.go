package ctrlproto

import "context"

// The user persona on the wire — who the user is *in the story* (a name plus a
// description), distinct from the agent-identity Persona. Session-scoped (the
// session rides the frame) and served by an OPTIONAL controller, so the verb
// does not ripple to every WorkspaceService implementer.
//
// The two halves take effect differently: the DESCRIPTION rides the uncached
// per-turn tail (like the author's note — no cache bust), while the NAME is the
// card {{user}} macro baked into the cached prefix, so binding a new name is a
// deliberate prompt rebuild. The handler applies both.
type UserController interface {
	// UserBind sets (or, with empty fields, clears) the session's user persona.
	// Only meaningful for an immersive (chat/play) session.
	UserBind(ctx context.Context, sess string, p UserBindParams) error
}

// UserBindParams carries the user persona to bind. Name is the {{user}} macro
// (a prefix rebuild on change); Description rides the free per-turn tail. Ref
// names a SAVED persona (userpersonas.*) to apply instead — its stored fields
// win over the inline ones. The bind COPIES those fields into the session, so a
// later edit to the saved persona does not reach a scene already playing as it;
// re-binding is what carries a change over.
type UserBindParams struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Pronouns    string `json:"pronouns,omitempty"`
	Ref         string `json:"ref,omitempty"`
}
