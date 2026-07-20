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
	// Provider/Model optionally draft on a specific model instead of the session's
	// current one (Phase 7 per-generation routing). Empty = the session's model.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Target selects whose line to draft (Phase 6 directed authorship). "" or
	// "user" drafts the player's own reply (the default, and the only mode that
	// fills the composer); "actor" drafts a named character's line in their voice;
	// "narrator" drafts a narrative beat. TargetName is the actor's name and
	// TargetVoice a short description of them — for a walk-on not yet in the cast;
	// both are ignored unless Target=="actor". An actor/narrator draft is meant to
	// be posted with post.line, not typed as the player.
	Target      string `json:"target,omitempty"`
	TargetName  string `json:"target_name,omitempty"`
	TargetVoice string `json:"target_voice,omitempty"`
	// TargetCard optionally voices a LIBRARY CARD instead of a typed walk-on
	// (Worlds W1): a card ref the daemon resolves to the character's full voice
	// (name, description, personality, scenario) for a faithful in-voice draft.
	// Ignored unless Target=="actor"; when set it supersedes TargetVoice, and
	// TargetName defaults to the card's name.
	TargetCard string `json:"target_card,omitempty"`
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
