package workspace

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/session"
)

// webConfirmer is a session's tool-approval seam. Unlike ACP (which issues a
// single request to one editor), it BROADCASTS a permission-request event to
// every connected client and parks the turn goroutine until the first client
// answers via [Workspace.Approve] — or the turn is cancelled. This is the
// multi-device fan-out: any device can approve, and a permission_resolved event
// tells the others to dismiss their prompt.
type webConfirmer struct{ s *wsSession }

var _ permission.ConfirmerWithRequest = (*webConfirmer)(nil)
var _ permission.ConfirmerWithCall = (*webConfirmer)(nil)

// Confirm is the id-less fallback (a gate caller that knows no call id); it
// mints a unique park key so even two id-less asks can never collide.
func (c *webConfirmer) Confirm(ctx context.Context, toolName, preview string) permission.ConfirmDecision {
	return c.ConfirmWithRequest(ctx, permission.ConfirmRequest{Tool: toolName, Preview: preview})
}

// ConfirmWithCall is kept for gate versions that predate ConfirmWithRequest.
func (c *webConfirmer) ConfirmWithCall(ctx context.Context, toolName, preview, callID string) permission.ConfirmDecision {
	return c.ConfirmWithRequest(ctx, permission.ConfirmRequest{Tool: toolName, Preview: preview, CallID: callID})
}

// ConfirmWithRequest parks this one call under its own id. The id arrives
// from the gate — the model's tool-call id, or the unique id the
// host_tool_call / script-binding door minted — never from session state:
// the old curCallID side-channel assumed tools run one at a time, and a
// host_tool_call approval parked concurrently with a model call's overwrote
// its pending channel, wedging the loser until turn cancel
// (TestWebConfirmerConcurrentParksDistinct). The gate's derived grant
// scopes ride the broadcast so any client can offer the scoped "always
// allow <command>" option.
// ctx is this CALL's context. It used to read s.turnCtx off the session under
// the mutex, which answers a subtly different question — "the turn this session
// is running now" rather than "the turn this call belongs to" — and answered nil
// for any door that ran outside a turn, leaving the park with nothing to cancel
// it.
func (c *webConfirmer) ConfirmWithRequest(ctx context.Context, cr permission.ConfirmRequest) permission.ConfirmDecision {
	s := c.s
	callID := cr.CallID
	var (
		ch      <-chan permission.ConfirmDecision
		release func()
	)
	if callID != "" {
		var ok bool
		ch, release, ok = s.permPark.Park(callID)
		if !ok {
			// A colliding id must never displace a live park (the pre-#408
			// wedge); fall through and mint a unique one instead.
			callID = ""
		}
	}
	if callID == "" {
		callID = fmt.Sprintf("call-%d", atomic.AddUint64(&s.askSeq, 1))
		ch, release, _ = s.permPark.Park(callID)
	}

	req := ctrlproto.PermissionRequest{
		CallID:  callID,
		Tool:    cr.Tool,
		Preview: cr.Preview,
		Scopes:  ctrlproto.GrantScopesFromCore(cr.Scopes),
	}
	s.openPermission(req)
	defer func() {
		release()
		s.closePermission(callID)
		s.broadcast(ctrlproto.PermissionResolvedEvent(callID))
	}()

	asked := time.Now()
	s.broadcast(ctrlproto.PermissionEvent(req))

	select {
	case d := <-ch:
		// Recorded only when a person decided: a cancelled prompt is not an
		// exchange, and a replay that showed one resolving would lie.
		s.recordPermission(req, d, asked)
		return d
	case <-ctx.Done():
		// Cancelled (client cancel / shutdown): fail closed.
		return permission.ConfirmDecision{Allow: false, Reason: "cancelled"}
	}
}

// recordPermission writes the exchange to the transcript so a replay can show
// the prompt, the pause, and the decision (session.PermissionRecord). A write
// failure is logged and never fails the decision: the tool call is what the
// person is waiting on, and the row is for a later reader.
func (s *wsSession) recordPermission(req ctrlproto.PermissionRequest, d permission.ConfirmDecision, asked time.Time) {
	err := s.sess.AppendPermission(session.PermissionRecord{
		CallID:  req.CallID,
		Tool:    req.Tool,
		Preview: req.Preview,
		Asked:   asked.UTC(),
		Waited:  time.Since(asked),
		Allow:   d.Allow,
		Reason:  d.Reason,
		Scope:   session.PermissionScopeOf(d),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "session %s: could not record the permission exchange: %v\n", s.id, err)
	}
}

// workerConfirmer routes a WORKER's tool-approval request to the dispatching
// session's human card — the "terva as caller" seam. A foreign worker that asks
// for permission (over its backend's ask/approve carrier) lands on the same card
// the session's own tool calls do, so one human answers for both.
//
// Unlike webConfirmer — keyed by the call id the gate passes down — it mints
// a stable per-request callID namespaced by the agent id, so several workers'
// concurrent asks never collide in pendPerm.
//
// It waits on TWO lifetimes, because a worker's approval sits between them: the
// ctx passed in is the WORKER's (the runner cancels it when the worker is
// stopped — a worker is not the session's turn, and must not be unparked by a
// turn ending), and c.ctx is the daemon's, which outlives every worker and
// unparks the wait on shutdown. The runner used to supply the first half itself
// by calling Confirm on a goroutine and selecting around it, which left a parked
// goroutine per unanswered ask.
type workerConfirmer struct {
	s       *wsSession
	ctx     context.Context // daemon lifetime; unblocks a parked wait on shutdown
	agentID string
	seq     atomic.Uint64
}

var _ permission.Confirmer = (*workerConfirmer)(nil)

func (c *workerConfirmer) Confirm(ctx context.Context, toolName, preview string) permission.ConfirmDecision {
	s := c.s
	callID := fmt.Sprintf("worker-%s-%d", c.agentID, c.seq.Add(1))
	// Agent carries the worker id as a first-class field so a board can
	// correlate this ask to the worker's lane tile; the callID prefix and the
	// "worker <id>:" preview stay as human-facing labeling, not the contract.
	req := ctrlproto.PermissionRequest{CallID: callID, Tool: toolName, Preview: preview, Agent: c.agentID}
	return parkWorkerAsk(ctx, c.ctx, s, req, func() { s.openPermission(req) })
}

// parkWorkerAsk parks a worker's ask on s, opens its card with open, and
// waits for the answer, the worker's stop, or the daemon's end.
func parkWorkerAsk(ctx, daemon context.Context, s *wsSession, req ctrlproto.PermissionRequest, open func()) permission.ConfirmDecision {
	ch, release, ok := s.permPark.Park(req.CallID)
	if !ok {
		return permission.ConfirmDecision{Allow: false, Reason: "the ask id " + req.CallID + " is already waiting"}
	}
	open()
	defer func() {
		release()
		s.closePermission(req.CallID)
		s.broadcast(ctrlproto.PermissionResolvedEvent(req.CallID))
	}()

	s.broadcast(ctrlproto.PermissionEvent(req))
	select {
	case d := <-ch:
		return d
	case <-ctx.Done():
		return permission.ConfirmDecision{Allow: false, Reason: "worker stopped before the approval was answered"}
	case <-daemon.Done():
		return permission.ConfirmDecision{Allow: false, Reason: "cancelled (session ending)"}
	}
}

// webAsker is a session's mid-turn question seam, mirroring webConfirmer:
// broadcast an ask-request, park until the first [Workspace.Answer].
type webAsker struct{ s *wsSession }

var (
	_ core.Asker       = (*webAsker)(nil)
	_ tools.CitedAsker = (*webAsker)(nil)
)

// Ask asks and records nothing in a talkoot's room. Its callers, such as
// ticket_init and the raati clerk, cannot hand a reference on, so a room line
// would only widen where the answer goes.
func (a *webAsker) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	return a.ask(ctx, qs)
}

// AskCited asks, and records the answer in the room when the session holds a
// Talkoot seat. ask_user_question is its caller, and it tells the member the
// reference.
func (a *webAsker) AskCited(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, tools.AnswerRecord, error) {
	ans, err := a.ask(ctx, qs)
	if err != nil || len(qs) == 0 {
		return ans, tools.AnswerRecord{}, err
	}
	return ans, a.s.recordTalkootAnswer(qs, ans), nil
}

func (a *webAsker) ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	s := a.s
	if len(qs) == 0 {
		return nil, nil
	}
	askID := fmt.Sprintf("ask_%d", atomic.AddUint64(&s.askSeq, 1))

	req := ctrlproto.NewAskRequest(askID, qs)
	ch, release, _ := s.askPark.Park(askID) // minted seq: never collides
	s.openAsk(req)
	defer func() {
		release()
		s.closeAsk(askID)
		s.broadcast(ctrlproto.AskResolvedEvent(askID))
	}()

	asked := time.Now()
	s.broadcast(ctrlproto.AskEvent(req))

	select {
	case ans := <-ch:
		// A client is free to send a short set (an older one answers only
		// the first question); the Asker contract to core is one answer
		// per question, so square it here rather than at every caller.
		padded := core.PadAnswers(ans, len(qs))
		s.recordAsk(askID, qs, padded, asked)
		return padded, nil
	case <-ctx.Done():
		return core.PadAnswers(nil, len(qs)), ctx.Err()
	}
}

// approve delivers a decision to a parked webConfirmer. First answer wins; a
// decision for an unknown/already-resolved call is a harmless no-op.
func (s *wsSession) approve(callID string, d permission.ConfirmDecision) {
	s.permPark.Deliver(callID, d)
}

// answer delivers an answer set to a parked webAsker. First answer wins.
func (s *wsSession) answer(askID string, answers []core.UserAnswer) {
	s.askPark.Deliver(askID, answers)
}

// recordAsk is recordPermission's twin for a question set.
func (s *wsSession) recordAsk(askID string, qs []core.UserQuestion, ans []core.UserAnswer, asked time.Time) {
	err := s.sess.AppendAsk(session.AskRecord{
		AskID:     askID,
		Questions: session.RecordQuestions(qs),
		Answers:   session.RecordAnswers(ans),
		Asked:     asked.UTC(),
		Waited:    time.Since(asked),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "session %s: could not record the ask exchange: %v\n", s.id, err)
	}
}
