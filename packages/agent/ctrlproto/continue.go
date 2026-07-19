package ctrlproto

import "context"

// The "continue" interaction on the wire — extend the trailing assistant message
// (a prefill continuation), the Stage counterpart to retry/swipe/edit. It is
// served by an OPTIONAL controller because it is capability-gated: only a
// provider whose wire format continues an assistant prefill (Anthropic today)
// can serve it, and a replay carrier never can. A carrier that does not
// implement it returns CodeUnsupported; a carrier that does still rejects the
// call (CodeBadRequest) when the session's provider or transcript can't continue.
type ContinueController interface {
	// ContinueTurn extends the session's trailing assistant message in place: it
	// runs one turn with that message as a provider prefill and merges the
	// streamed continuation onto it. epoch guards staleness like the other
	// revision verbs (CodeConflict on mismatch); CodeBusy when a turn is running;
	// CodeBadRequest when there is nothing to continue or the provider can't.
	ContinueTurn(ctx context.Context, sess string, epoch uint64) error
}

// TurnContinueParams is the payload of [MethodTurnContinue]: the client's
// transcript epoch, checked exactly as the other revision verbs check it.
type TurnContinueParams struct {
	Epoch uint64 `json:"epoch"`
}
