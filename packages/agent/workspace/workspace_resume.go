package workspace

import (
	"context"
	"sync"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The "resume" interaction: run the loop again for a session whose last turn died
// without producing a reply. Unlike retry it throws nothing away, and unlike
// prompt it adds nothing. Everything the session needs is already on the
// transcript; only the request that reads it is missing.
//
// It is served by the base service rather than an optional controller, because
// two of its three shapes run an ordinary turn on any provider at all. See the
// dispatch entry for MethodTurnResume.

// ResumeTurn runs the agent loop against a stranded session's transcript.
func (w *Workspace) ResumeTurn(_ context.Context, sess string, p ctrlproto.TurnResumeParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.resumeTurn(p.Epoch)
}

// resumeTurn routes on the shape of the transcript rather than asking the client
// to name it. A client that had to decide between "run a turn" and "extend the
// last message" would be reimplementing [core.ResumeStateOf] against a transcript
// it may have read before a restart, and there are two front ends to get that
// wrong independently.
//
// Same epoch and busy guards as the other revision verbs.
func (s *wsSession) resumeTurn(epoch uint64) error {
	s.revisionMu.Lock()
	release := sync.OnceFunc(s.revisionMu.Unlock)
	defer release()
	// Epoch 0 is "I do not track transcript revisions", the convention
	// conversation.history already uses. The TUI is that client. Resume can take
	// it because it names no index: there is nothing a stale view could point at
	// wrongly, and the classification below reads the live transcript regardless.
	var guard *uint64
	if epoch != 0 {
		guard = &epoch
	}
	if err := s.revisionGuard(guard); err != nil {
		return err
	}

	state := core.ResumeStateOf(s.agent.Messages())
	if !state.Stuck() {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s",
			i18n.T("this session is not waiting on a reply, so there is nothing to resume"))
	}
	// Only the cut-short shape needs the prefill capability, and refusing it by
	// name beats refusing the whole verb: the other two shapes are exactly the
	// ones a provider without prefill support still gets to use.
	if state == core.ResumeAfterCutShort && !s.agent.ContinuesAssistantPrefill() {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s",
			i18n.T("the last reply stopped partway, and this session's provider cannot continue an unfinished message"))
	}

	turnCtx, err := s.beginTurnHeld()
	if err != nil {
		return err
	}
	// The tail span is the swipeable variant set. Every shape here commits it: a
	// resumed turn produces the response this transcript never had, not an
	// alternative to one it already has. So clear it and do not reseed, which is
	// what continue does and what retry deliberately does not.
	s.clearTail()
	release()

	if state == core.ResumeAfterCutShort {
		// Extends the trailing assistant message in place, so it persists through
		// the same replace amend as turn.continue.
		s.launchTurn(turnCtx, func(ctx context.Context) error {
			return s.agent.ContinueAssistant(ctx, nil)
		}, s.persistContinue)
		return nil
	}

	// A trailing prompt or trailing tool results: an ordinary turn against the
	// transcript as it stands. No afterTurn, because the agent's own append path
	// persists the reply exactly as it does for a prompt.
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		return s.agent.Continue(ctx, nil)
	}, nil)
	return nil
}
