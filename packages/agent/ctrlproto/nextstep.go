package ctrlproto

import "context"

// A suggested next line on the wire — stage 3 of
// docs/proposals/idle-suggestions.md.
//
// The client notices the user has gone quiet with an empty composer; the model,
// the credential and the transcript are all daemon-side. So the client asks,
// and the daemon answers with one line the user might type next. The client
// offers it as ghost text; nothing is sent unless the user chooses to send it.
//
// No params: the session rides the frame, and the question is always the same
// one. Served by an OPTIONAL controller, like suggest.reply beside it, so the
// verb does not ripple out to every WorkspaceService implementer.
//
// GroupSession, with suggest.reply and the side-chat trio, rather than
// GroupConversation where shell.result sits. The distinction those two verbs
// draw is whether the caller can put anything in front of the model on a later
// turn: shell.result arms a block that rides the user's next request, so it
// belongs with prompt. This one records nothing, arms nothing, and its answer
// reaches the client alone — the session is read, not written.
type NextStepController interface {
	// SuggestNextStep returns one line the user might send next, or an empty
	// one when there is nothing worth offering.
	SuggestNextStep(ctx context.Context, sess string) (NextStepResult, error)
}

// NextStepResult carries the offered line.
//
// An empty Line is an ordinary answer rather than a failure: the daemon invites
// the model to stay quiet when no next step is obvious, and a client that has
// nothing to offer simply offers nothing.
type NextStepResult struct {
	// Line is a single line, already bounded by the daemon. A client is not
	// expected to trim it, and must not assume it is non-empty.
	Line string `json:"line"`
}
