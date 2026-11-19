package workspace

import (
	"context"
	"errors"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// errSentNow is the cause of the cancel that queue.send performs.
//
// 🔑 It is not core.ErrUserInterrupted, though a person asked for it. That
// cause withdraws a prompt the model has not answered, and a talkoot counts
// it as a stop. A send adds to the conversation: the first prompt stays, and
// the session goes straight on.
var errSentNow = errors.New("turn stopped to deliver a queued message")

// SendQueued delivers entries of the pending queue ahead of the running turn.
// See ctrlproto.WorkspaceService.
func (w *Workspace) SendQueued(ctx context.Context, sess string, p ctrlproto.QueueSendParams) error {
	if s := w.live(sess); s != nil {
		return s.sendQueued(p)
	}
	return nil
}

// sendQueued takes the entries that p names out of the queue and makes them
// the next turn. A running turn is stopped for them, and endTurn starts the
// new one. A running compaction is left to finish, and endCompacting starts
// it. An idle session starts it here.
//
// 🔑 The entries leave the queue under s.mu, before the cancel. A text that
// stayed queued could reach the dying turn at a safe boundary, and endTurn
// would then find nothing to restart on.
//
// 🔑 The cancel of one entry's send runs inside the take, under the queue's
// lock. The stopped turn then cannot take the entries that stay, at any
// boundary, and not even when it has yet to reach its loop
// (core.Agent.TakeQueuedMessage).
func (s *wsSession) sendQueued(p ctrlproto.QueueSendParams) error {
	s.mu.Lock()
	cancel := s.turnCancel
	stop := func() {
		if cancel != nil {
			cancel(errSentNow)
		}
	}
	var sent []core.QueuedMessage
	if p.All {
		sent = s.agent.DrainQueuedMessages()
		// With nothing taken there is nothing to stop the turn for.
		if len(sent) > 0 {
			stop()
		}
	} else if m, ok := s.agent.TakeQueuedMessage(p.Index, p.Text, stop); ok {
		sent = []core.QueuedMessage{m}
	} else {
		s.mu.Unlock()
		return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("the queue changed, so nothing was sent"))
	}
	if len(sent) == 0 {
		s.mu.Unlock()
		return nil
	}
	hold := !p.All
	if cancel == nil && !s.compacting {
		s.mu.Unlock()
		return s.startSent(sent, hold)
	}
	// ⚠️ A second send can arrive before the turn has ended. Both sets then
	// go, and the queue is held only when every send asked for one entry.
	s.sentHold = hold && (len(s.sent) == 0 || s.sentHold)
	s.sent = append(s.sent, sent...)
	s.mu.Unlock()
	s.broadcastQueue()
	return nil
}

// sentStart says how the turn that a send starts treats the queue.
type sentStart struct {
	// hold keeps the queue out of the turn's first boundary, and lead is how
	// many entries from its front that boundary still takes: the other
	// entries of the send. See core.UserMessageExtras.
	hold bool
	lead int
}

// takeSentHeld hands over the entries that a send holds for the next turn.
// The first one starts the turn, and the others go back to the front of the
// queue in their order, ahead of every text that waits. The caller holds
// s.mu.
func (s *wsSession) takeSentHeld() (next core.QueuedMessage, start sentStart, ok bool) {
	if len(s.sent) == 0 {
		return core.QueuedMessage{}, sentStart{}, false
	}
	sent, hold := s.sent, s.sentHold
	s.sent, s.sentHold = nil, false
	return sent[0], s.requeueSentRest(sent, hold), true
}

// requeueSentRest puts every entry of sent but the first at the front of the
// queue, and says how the turn on the first one treats the queue. With hold,
// that turn's first boundary takes those entries and no other.
func (s *wsSession) requeueSentRest(sent []core.QueuedMessage, hold bool) sentStart {
	lead := 0
	for i := len(sent) - 1; i >= 1; i-- {
		if s.agent.RequeueFront(sent[i]) {
			lead++
		}
	}
	return sentStart{hold: hold, lead: lead}
}

// restoreSentHeld puts held entries back at the front of the queue, for a
// session that cannot start a turn. The caller holds s.mu.
func (s *wsSession) restoreSentHeld() {
	for i := len(s.sent) - 1; i >= 0; i-- {
		s.agent.RequeueFront(s.sent[i])
	}
	s.sent, s.sentHold = nil, false
}

// startSent starts a turn on sent for a session that runs nothing.
func (s *wsSession) startSent(sent []core.QueuedMessage, hold bool) error {
	err := s.promptQueuedHold(sent[0], s.requeueSentRest(sent, hold))
	if err != nil {
		// The text waits at the front again, so no failure loses it.
		s.agent.RequeueFront(sent[0])
	}
	s.broadcastQueue()
	if errors.Is(err, ctrlproto.ErrBusy) {
		// Another prompt took the slot first, and that turn delivers the
		// text. Every other failure means that nothing will, so the caller
		// hears it.
		return nil
	}
	return err
}
