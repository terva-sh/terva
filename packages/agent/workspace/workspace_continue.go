package workspace

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The "continue" interaction — extend the trailing assistant message as a
// provider prefill. Served by the real Workspace as an optional controller
// because it is capability-gated: only a provider whose wire format continues an
// assistant prefill can run it. The gate is enforced in the handler (a clear bad
// request otherwise), so a client can call it optimistically.
var _ ctrlproto.ContinueController = (*Workspace)(nil)

// ContinueTurn extends a session's trailing assistant message in place.
func (w *Workspace) ContinueTurn(_ context.Context, sess string, epoch uint64) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.continueTurn(epoch)
}

// continueTurn runs one prefill-continuation turn: the agent extends the trailing
// assistant message in place (ContinueAssistant), and the merge is persisted as a
// replace amend once the turn seals. Same epoch/busy guards as the other revision
// verbs; a bad request when there is nothing to continue or the provider can't.
func (s *wsSession) continueTurn(epoch uint64) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	msgs := s.agent.Messages()
	n := len(msgs)
	if n == 0 || msgs[n-1].Role != provider.RoleAssistant {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "there is no response to continue")
	}
	if !s.agent.ContinuesAssistantPrefill() {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "this session's provider cannot continue an assistant message")
	}
	turnCtx, err := s.beginTurn()
	if err != nil {
		return err
	}
	// A continue grows the trailing message in place: like an edit it commits or
	// invalidates the swipe span and creates NO new variant, so clear the tail and
	// do NOT reseed it (unlike retry, which seeds a fresh take).
	s.clearTail()
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		return s.agent.ContinueAssistant(ctx, nil)
	}, s.persistContinue)
	return nil
}

// persistContinue is continueTurn's afterTurn: ContinueAssistant already merged
// the continuation onto the live transcript, so persist it durably as a replace
// amend (the walker reconstructs effective[idx] = merged on reload). No-op if the
// turn produced no merge (e.g. it errored before any text landed).
func (s *wsSession) persistContinue() {
	if idx, merged, ok := s.agent.ConsumeContinueResult(); ok {
		// idx is the in-memory index of the continued message; persist against the
		// on-disk index so a reload replaces the right row (see wsSession.diskIndex).
		if disk, ok := s.diskIndex(idx); ok {
			_ = s.sess.AppendAmend(core.AmendReplace, disk, &merged, "continue")
		}
	}
}
