package ctrlproto

import "context"

// The author's note on the wire — a per-session steering string injected into
// the UNCACHED per-turn tail (see build.NoteRecord), after a card's
// post_history_instructions. Like backgrounds.bind it is session-scoped (the
// session rides the frame) and served by an OPTIONAL controller, so the verb
// does not ripple to every WorkspaceService implementer.
type NoteController interface {
	// NoteSet sets (or, with an empty string, clears) the session's author's
	// note. Takes effect on the next turn with no cache bust; only meaningful for
	// an immersive (chat/play) session.
	NoteSet(ctx context.Context, sess string, p NoteSetParams) error
}

// NoteSetParams carries the new author's-note text for the session, or "" to
// clear it.
type NoteSetParams struct {
	Text string `json:"text"`
}
