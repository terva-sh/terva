package workspace

import (
	"context"
	"fmt"
	"sync/atomic"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// webConfirmer is a session's tool-approval seam. Unlike ACP (which issues a
// single request to one editor), it BROADCASTS a permission-request event to
// every connected client and parks the turn goroutine until the first client
// answers via [Workspace.Approve] — or the turn is cancelled. This is the
// multi-device fan-out: any device can approve, and a permission_resolved event
// tells the others to dismiss their prompt.
type webConfirmer struct{ s *wsSession }

var _ core.ConfirmerWithRequest = (*webConfirmer)(nil)
var _ core.ConfirmerWithCall = (*webConfirmer)(nil)

// Confirm is the id-less fallback (a gate caller that knows no call id); it
// mints a unique park key so even two id-less asks can never collide.
func (c *webConfirmer) Confirm(toolName, preview string) core.ConfirmDecision {
	return c.ConfirmWithRequest(core.ConfirmRequest{Tool: toolName, Preview: preview})
}

// ConfirmWithCall is kept for gate versions that predate ConfirmWithRequest.
func (c *webConfirmer) ConfirmWithCall(toolName, preview, callID string) core.ConfirmDecision {
	return c.ConfirmWithRequest(core.ConfirmRequest{Tool: toolName, Preview: preview, CallID: callID})
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
func (c *webConfirmer) ConfirmWithRequest(cr core.ConfirmRequest) core.ConfirmDecision {
	s := c.s
	s.mu.Lock()
	turnCtx := s.turnCtx
	s.mu.Unlock()

	callID := cr.CallID
	var (
		ch      <-chan core.ConfirmDecision
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
	s.mu.Lock()
	s.permReq[callID] = req
	s.mu.Unlock()
	defer func() {
		release()
		s.mu.Lock()
		delete(s.permReq, callID)
		s.mu.Unlock()
		s.broadcast(ctrlproto.PermissionResolvedEvent(callID))
	}()

	s.broadcast(ctrlproto.PermissionEvent(req))

	var done <-chan struct{}
	if turnCtx != nil {
		done = turnCtx.Done()
	}
	select {
	case d := <-ch:
		return d
	case <-done:
		// Cancelled (client cancel / shutdown): fail closed.
		return core.ConfirmDecision{Allow: false, Reason: "cancelled"}
	}
}

// workerConfirmer routes a WORKER's tool-approval request to the dispatching
// session's human card — the "terva as caller" seam. A foreign worker that asks
// for permission (over its backend's ask/approve carrier) lands on the same card
// the session's own tool calls do, so one human answers for both.
//
// Unlike webConfirmer — keyed by the call id the gate passes down — it mints
// a stable per-request callID namespaced by the agent id, so several workers'
// concurrent asks never collide in pendPerm. It parks on the
// daemon context rather than a turn context because a worker is not the session's
// turn; the runner's handleAsk separately abandons the wait if the worker is
// stopped first (racing this Confirm against the worker's own context).
type workerConfirmer struct {
	s       *wsSession
	ctx     context.Context // daemon lifetime; unblocks a parked wait on shutdown
	agentID string
	seq     atomic.Uint64
}

var _ core.Confirmer = (*workerConfirmer)(nil)

func (c *workerConfirmer) Confirm(toolName, preview string) core.ConfirmDecision {
	s := c.s
	callID := fmt.Sprintf("worker-%s-%d", c.agentID, c.seq.Add(1))
	// Agent carries the worker id as a first-class field so a board can
	// correlate this ask to the worker's lane tile; the callID prefix and the
	// "worker <id>:" preview stay as human-facing labeling, not the contract.
	req := ctrlproto.PermissionRequest{CallID: callID, Tool: toolName, Preview: preview, Agent: c.agentID}
	ch, release, _ := s.permPark.Park(callID) // per-agent seq: never collides
	s.mu.Lock()
	s.permReq[callID] = req
	s.mu.Unlock()
	defer func() {
		release()
		s.mu.Lock()
		delete(s.permReq, callID)
		s.mu.Unlock()
		s.broadcast(ctrlproto.PermissionResolvedEvent(callID))
	}()

	s.broadcast(ctrlproto.PermissionEvent(req))
	select {
	case d := <-ch:
		return d
	case <-c.ctx.Done():
		return core.ConfirmDecision{Allow: false, Reason: "cancelled (session ending)"}
	}
}

// webAsker is a session's mid-turn question seam, mirroring webConfirmer:
// broadcast an ask-request, park until the first [Workspace.Answer].
type webAsker struct{ s *wsSession }

var _ core.Asker = (*webAsker)(nil)

func (a *webAsker) Ask(ctx context.Context, q core.UserQuestion) (core.UserAnswer, error) {
	s := a.s
	askID := fmt.Sprintf("ask_%d", atomic.AddUint64(&s.askSeq, 1))

	req := ctrlproto.AskRequest{AskID: askID, Question: q.Question, Options: q.Options, AllowCustom: q.AllowCustom}
	ch, release, _ := s.askPark.Park(askID) // minted seq: never collides
	s.mu.Lock()
	s.askReq[askID] = req
	s.mu.Unlock()
	defer func() {
		release()
		s.mu.Lock()
		delete(s.askReq, askID)
		s.mu.Unlock()
		s.broadcast(ctrlproto.AskResolvedEvent(askID))
	}()

	s.broadcast(ctrlproto.AskEvent(req))

	select {
	case ans := <-ch:
		return ans, nil
	case <-ctx.Done():
		return core.UserAnswer{Declined: true}, ctx.Err()
	}
}

// approve delivers a decision to a parked webConfirmer. First answer wins; a
// decision for an unknown/already-resolved call is a harmless no-op.
func (s *wsSession) approve(callID string, d core.ConfirmDecision) {
	s.permPark.Deliver(callID, d)
}

// answer delivers an answer to a parked webAsker. First answer wins.
func (s *wsSession) answer(askID string, a core.UserAnswer) {
	s.askPark.Deliver(askID, a)
}
