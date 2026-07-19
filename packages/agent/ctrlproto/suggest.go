package ctrlproto

import "context"

// SuggestController is OPTIONAL (like DoctorController): the "suggest a reply"
// drafting aid. Given a live session and the user's guidance, it drafts the
// human player's next message in their voice — a composer aid that never touches
// the transcript. Served only by the real Workspace (it needs a model and the
// session's transcript); a carrier without one answers "unsupported" rather than
// failing deeper. Stage-only today, so it rides ctrlclient's notForwarded
// allow-list rather than the Go forwarder.
type SuggestController interface {
	// SuggestReply drafts the player's next message. The session is the frame's
	// sess. History carries the prior back-and-forth (each Note→Draft round) so
	// the daemon holds no per-request state; the daemon builds the drafting prompt
	// from the session's transcript and user persona each call.
	SuggestReply(ctx context.Context, sess string, p SuggestParams) (SuggestResult, error)
}

// SuggestParams is the payload of suggest.reply. Note is the new guidance to
// draft against — the first Note is the user's opening sketch, which may be
// empty for a cold suggestion. History is the refinement so far.
type SuggestParams struct {
	History []SuggestTurn `json:"history,omitempty"`
	Note    string        `json:"note,omitempty"`
}

// SuggestTurn is one completed round of the drafting back-and-forth: the
// guidance the user gave (Note, possibly empty) and the draft the model returned
// (Draft). Carried on every call so the daemon stays stateless, mirroring
// SideChatTurn.
type SuggestTurn struct {
	Note  string `json:"note,omitempty"`
	Draft string `json:"draft"`
}

// SuggestResult is the payload of a suggest.reply response: the drafted next
// user message, ready to drop into the composer (the user still sends it).
type SuggestResult struct {
	Draft string `json:"draft"`
}
