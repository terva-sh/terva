package ctrlproto

import "context"

// DirectController is OPTIONAL: Stage's directed-authorship commit (Phase 6) —
// the "post" half of the suggest → review → edit → post loop.
//
// Where SuggestController drafts a line and never touches the transcript,
// DirectController posts an APPROVED line into the transcript as an attributed
// assistant message — a character's or the narrator's turn the user authored at
// their reply boundary. It is not a model turn: the text was already drafted and
// approved (via suggest), so the daemon only appends, persists, and broadcasts.
//
// Served only by the real Workspace (it needs a live session); a carrier without
// one answers "unsupported" rather than failing deeper. Stage-only today, so it
// rides ctrlclient's notForwarded allow-list rather than the Go forwarder.
type DirectController interface {
	// PostLine commits an approved directed line to the session's transcript. The
	// session is the frame's sess. It appends an assistant message attributed to
	// an actor (or the narrator), never runs the model, and rejects with CodeBusy
	// while a turn is in flight.
	PostLine(ctx context.Context, sess string, p PostLineParams) error

	// DirectTurn runs ONE turn steered by an out-of-character direction — the
	// "apply-as-direction" disposition (Phase 6b). Unlike PostLine, this DOES run
	// the model: the direction rides the transcript as a [Direction] message (the
	// same convention cast.speak uses) and the model writes the next beat
	// following it. Chat or play; CodeBusy while a turn is in flight.
	DirectTurn(ctx context.Context, sess string, p DirectTurnParams) error

	// AdvanceTurn runs ONE turn on the transcript exactly as it stands — no user
	// message, no direction, no parameters. It is the "just go" knob: the scene
	// moves on because it is plainly someone else's turn, which is the state a run
	// of PostLine calls leaves it in.
	//
	// It differs from DirectTurn in what it PERSISTS: DirectTurn writes a visible
	// [Direction] message into the scene, AdvanceTurn writes nothing but the
	// model's reply (its stage cue is request-scoped). It differs from turn.continue
	// in what it PRODUCES: continue extends the trailing assistant message in place
	// (an Anthropic prefill), advance writes the next beat as a new message on every
	// provider. CodeBusy while a turn is in flight.
	AdvanceTurn(ctx context.Context, sess string) error
}

// PostLineParams is the payload of post.line. Actor is the speaking character's
// name; empty posts a narrator beat. Text is the approved line — the daemon
// trims it and rejects an empty post.
type PostLineParams struct {
	Actor string `json:"actor,omitempty"`
	Text  string `json:"text"`
}

// DirectTurnParams is the payload of direct.turn (Phase 6b). Text is the
// out-of-character direction — what should happen next; the daemon wraps it in
// the [Direction] marker, trims it, and rejects an empty direction.
type DirectTurnParams struct {
	Text string `json:"text"`
}
