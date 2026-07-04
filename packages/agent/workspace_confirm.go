package agent

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

var _ core.Confirmer = (*webConfirmer)(nil)

func (c *webConfirmer) Confirm(toolName, preview string) core.ConfirmDecision {
	s := c.s
	s.mu.Lock()
	callID := s.curCallID
	turnCtx := s.turnCtx
	s.mu.Unlock()
	if callID == "" {
		callID = "call" // fallback; recordCall normally sets the real id
	}

	req := ctrlproto.PermissionRequest{CallID: callID, Tool: toolName, Preview: preview}
	ch := make(chan core.ConfirmDecision, 1)
	s.mu.Lock()
	s.pendPerm[callID] = ch
	s.permReq[callID] = req
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendPerm, callID)
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

// webAsker is a session's mid-turn question seam, mirroring webConfirmer:
// broadcast an ask-request, park until the first [Workspace.Answer].
type webAsker struct{ s *wsSession }

var _ core.Asker = (*webAsker)(nil)

func (a *webAsker) Ask(ctx context.Context, q core.UserQuestion) (core.UserAnswer, error) {
	s := a.s
	askID := fmt.Sprintf("ask_%d", atomic.AddUint64(&s.askSeq, 1))

	req := ctrlproto.AskRequest{AskID: askID, Question: q.Question, Options: q.Options, AllowCustom: q.AllowCustom}
	ch := make(chan core.UserAnswer, 1)
	s.mu.Lock()
	s.pendAsk[askID] = ch
	s.askReq[askID] = req
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendAsk, askID)
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
	s.mu.Lock()
	ch := s.pendPerm[callID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- d:
		default:
		}
	}
}

// answer delivers an answer to a parked webAsker. First answer wins.
func (s *wsSession) answer(askID string, a core.UserAnswer) {
	s.mu.Lock()
	ch := s.pendAsk[askID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- a:
		default:
		}
	}
}
