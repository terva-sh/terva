package workspace

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The talkoot inbox: every question and approval that a member of a talkoot
// waits on.
//
// 🔑 The inbox keeps no cards of its own. A session already holds each open
// card for its snapshot (permReq, askReq), and the inbox reads those. The park
// that holds a card also closes it, so the member's view and the inbox resolve
// together, and no second store can keep a card that was answered.
//
// 🔑 A card's two events cannot cross. The goroutine that parks a card sends
// the open before it waits for the answer, and the close runs in its deferred
// release after the wait. An answer that lands between the map write and the
// open event only fills the park's channel, so the close still comes second.
//
// 🔑 An answer reaches a card only through Workspace.Approve and
// Workspace.Answer, the approve and answer verbs of a ctrlproto client. The
// router holds its drivers, which deliver envelopes and never answer.

// cardKey names an open card. A call id and an ask id come from different
// sequences, so the kind is part of the key.
type cardKey struct {
	ask bool
	id  string
}

// openCard is when a card opened, and the seat that owned the session then.
// talkoot is empty for a session with no seat.
type openCard struct {
	at              time.Time
	talkoot, member string
}

// openPermission records a pending approval for the snapshot, and shows it in
// the inbox of the session's talkoot.
func (s *wsSession) openPermission(req ctrlproto.PermissionRequest) {
	c := s.cardSeat()
	s.mu.Lock()
	s.permReq[req.CallID] = req
	s.setCardLocked(cardKey{id: req.CallID}, c)
	s.mu.Unlock()
	s.ws.talkootCardEvent(c, ctrlproto.TalkootInboxEvent(c.talkoot, s.permissionCard(c, req)))
}

// closePermission removes a pending approval from the snapshot and the inbox.
func (s *wsSession) closePermission(callID string) {
	k := cardKey{id: callID}
	s.mu.Lock()
	delete(s.permReq, callID)
	c := s.cards[k]
	delete(s.cards, k)
	s.mu.Unlock()
	s.ws.talkootCardEvent(c, ctrlproto.TalkootInboxResolvedEvent(c.talkoot, s.cardHead(c, ctrlproto.TalkootCardPermission, callID)))
}

// openAsk is openPermission for a question set.
func (s *wsSession) openAsk(req ctrlproto.AskRequest) {
	c := s.cardSeat()
	s.mu.Lock()
	s.askReq[req.AskID] = req
	s.setCardLocked(cardKey{ask: true, id: req.AskID}, c)
	s.mu.Unlock()
	s.ws.talkootCardEvent(c, ctrlproto.TalkootInboxEvent(c.talkoot, s.askCard(c, req)))
}

// closeAsk is closePermission for a question set.
func (s *wsSession) closeAsk(askID string) {
	k := cardKey{ask: true, id: askID}
	s.mu.Lock()
	delete(s.askReq, askID)
	c := s.cards[k]
	delete(s.cards, k)
	s.mu.Unlock()
	s.ws.talkootCardEvent(c, ctrlproto.TalkootInboxResolvedEvent(c.talkoot, s.cardHead(c, ctrlproto.TalkootCardAsk, askID)))
}

// cardSeat stamps a card with the time and the seat that holds the session.
func (s *wsSession) cardSeat() openCard {
	c := openCard{at: time.Now()}
	if s.ws == nil {
		return c
	}
	if seat, ok := s.ws.talkootSeatOf(s.id); ok && seat.b != nil {
		c.talkoot, c.member = seat.b.run.id, seat.b.member
	}
	return c
}

func (s *wsSession) setCardLocked(k cardKey, c openCard) {
	if s.cards == nil {
		s.cards = map[cardKey]openCard{}
	}
	s.cards[k] = c
}

func (s *wsSession) cardHead(c openCard, kind, id string) ctrlproto.TalkootCard {
	return ctrlproto.TalkootCard{Session: s.id, Member: c.member, Kind: kind, ID: id}
}

func (s *wsSession) permissionCard(c openCard, req ctrlproto.PermissionRequest) ctrlproto.TalkootCard {
	card := s.cardHead(c, ctrlproto.TalkootCardPermission, req.CallID)
	card.At, card.Permission = c.at, &req
	return card
}

func (s *wsSession) askCard(c openCard, req ctrlproto.AskRequest) ctrlproto.TalkootCard {
	card := s.cardHead(c, ctrlproto.TalkootCardAsk, req.AskID)
	card.At, card.Ask = c.at, &req
	return card
}

// inboxCards returns the session's open cards that belong to talkoot id.
func (s *wsSession) inboxCards(id string) []ctrlproto.TalkootCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ctrlproto.TalkootCard
	for k, c := range s.cards {
		if c.talkoot != id {
			continue
		}
		if k.ask {
			if req, ok := s.askReq[k.id]; ok {
				out = append(out, s.askCard(c, req))
			}
		} else if req, ok := s.permReq[k.id]; ok {
			out = append(out, s.permissionCard(c, req))
		}
	}
	return out
}

// talkootCardEvent sends a card's event to the watchers of the talkoot that
// owned the card when it opened. A card that opened outside a seat sends
// nothing.
//
// 🔑 The close goes to the talkoot of the open, not to the session's seat now.
// A member that leaves the roster keeps its session and its open cards, and
// a watcher that saw the card open must see it close.
func (w *Workspace) talkootCardEvent(c openCard, ev ctrlproto.Event) {
	if w == nil || c.talkoot == "" {
		return
	}
	w.talkootEmit(talkootEvent{Talkoot: c.talkoot, Kind: "inbox", Wire: &ev})
}

// talkootInbox returns every open card of a talkoot, oldest first: the
// questions and approvals its members wait on, and its pending proposals.
// It reads every live session, and not only the seated ones, so a member that
// left the roster still shows the cards it waits on.
func (w *Workspace) talkootInbox(ctx context.Context, id string) ([]ctrlproto.TalkootCard, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	all := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		all = append(all, s)
	}
	w.mu.Unlock()
	out := append([]ctrlproto.TalkootCard{}, w.proposalCards(id, run.dir)...)
	for _, s := range all {
		out = append(out, s.inboxCards(id)...)
	}
	slices.SortFunc(out, func(a, b ctrlproto.TalkootCard) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.Session, b.Session), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}
