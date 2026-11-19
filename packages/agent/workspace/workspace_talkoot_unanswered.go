package workspace

// A question that nobody answers (decision 0029 rule 12).
//
// A turn in a chain that a routine began may have no person to ask. A
// question or an approval of such a turn gets a deadline, from
// talkoot_routines.question_wait. At the deadline the card closes as expired
// and the chain ends:
//
//   - A run is marked. Its next tool call stops it, and its end is a note to
//     its member that holds the question (routineRun.unanswered).
//   - A member's chain is paused in the router (talkoot.Router.Unanswered),
//     and the member's turn is stopped at its next tool call. One call
//     passes: a talkoot_send, which the router lets go to the person alone.
//
// 🚨 The host answers nothing for the person. The model reads that nobody
// answered, and what ends. A question that the host declined for the person
// would let the turn go on, and the agent would then learn to work around a
// person who is away.
//
// 🔑 A turn that a person started keeps the rule it had: its card waits
// until the turn ends. A person began that work, and may be at the keyboard.

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
)

// questionWait reads how long a card of an unattended turn waits. A test
// shortens it, because the key takes no value under one minute.
var questionWait = func() time.Duration { return config.TalkootRoutines().Wait() }

// leftWordTool is the one tool that a member's turn may still call after
// its question had no answer.
const leftWordTool = "talkoot_send"

// unattendedTurn is a turn at work in a chain that a routine began.
type unattendedTurn struct {
	sess *wsSession
	seat talkootSeat
	// root is the chain. run is set when the turn is a run's own.
	root string
	run  *routineRun
	wait time.Duration
}

// deliveredOnly reports whether the turn at work took input and none of it
// from a person. A turn that a person typed into is attended, whatever chain
// its member holds.
//
// 🚨 A report on sub-agents is such input too. A member in a chain that no
// person began could otherwise start a sub-agent with a small task, and then
// work with a person's authority in the turn that the report begins
// (TKT-01M7R5QJ).
func (r *talkootReads) deliveredOnly() bool {
	fed := r.taken.Load() - r.turnTaken.Load() + r.recaps.Load() - r.turnRecaps.Load()
	return fed > 0 && r.users.Load()-r.turnUsers.Load() == fed
}

// unattended reports whether the turn that this session runs is unattended.
func (s *wsSession) unattended() (unattendedTurn, bool) {
	if s.ws == nil {
		return unattendedTurn{}, false
	}
	seat, ok := s.ws.talkootSeatOf(s.id)
	if !ok || seat.b == nil {
		return unattendedTurn{}, false
	}
	u := unattendedTurn{sess: s, seat: seat, wait: questionWait()}
	if rr := seat.b.routine; rr != nil {
		u.root, u.run = rr.run.ID, rr
		return u, true
	}
	if !s.reads.deliveredOnly() {
		return unattendedTurn{}, false
	}
	u.root, ok = seat.unattendedRoot()
	return u, ok
}

// unattendedRoot returns the chain that the seat's member works in, when a
// routine began it.
func (s talkootSeat) unattendedRoot() (root string, ok bool) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return "", false
	}
	err := s.b.run.do(func(rt *talkoot.Router) error {
		if !s.b.retired.Load() {
			root, ok = rt.Unattended(s.b.member)
		}
		return nil
	})
	return root, ok && err == nil
}

// deadline returns the channel that fires at the turn's deadline, and the
// call that frees the timer. A turn that is attended gets a channel that
// never fires.
//
// 🔑 A run's time limit does not count the wait. The limit exists because
// nothing else ended a wait for a person, and the deadline now does. So the
// limit's timer stops while a card of the run is open.
func (u unattendedTurn) deadline(unattended bool) (<-chan time.Time, func()) {
	if !unattended {
		return nil, func() {}
	}
	t := time.NewTimer(u.wait)
	if u.run != nil {
		u.run.hold()
	}
	return t.C, func() {
		t.Stop()
		if u.run != nil {
			u.run.unhold()
		}
	}
}

// expired ends the chain at a card that nobody answered, and returns what
// the model reads. asked is what the card asked for.
func (u unattendedTurn) expired(asked string) string {
	wait := wakeSpan(u.wait)
	if u.run != nil {
		u.run.unanswered.CompareAndSwap(nil, &asked)
		return fmt.Sprintf("no person answered in %s. terva answers nothing for a person, so this run ends here. "+
			"Call no more tools. Write what you left undone as your last message. It goes to your own session with the question.", wait)
	}
	s := u.seat
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	ended := false
	err := s.b.run.do(func(rt *talkoot.Router) (err error) {
		if s.b.revoked || s.b.retired.Load() {
			return errSeatRevoked
		}
		ended, err = rt.Unanswered(s.b.member, u.root, asked, u.wait)
		return err
	})
	if err != nil {
		s.w.diagf("talkoot %s: the chain %s of %s did not end at a question with no answer: %v", s.b.run.id, u.root, s.b.member, err)
	}
	if !ended {
		// The member left the chain while it waited, and works in another
		// one now. The card is closed all the same, and nobody answered it.
		return fmt.Sprintf("no person answered in %s. terva answers nothing for a person. Do not go on with the work that needs this answer.", wait)
	}
	// 🚨 The mark is set before the model reads the text, so its next tool
	// call finds it.
	u.sess.unanswered.Store(true)
	return fmt.Sprintf("no person answered in %s. No person began this chain, and terva answers nothing for a person, so the chain ends here. "+
		"terva paused it, and a person can resume it. Do no more work in it. "+
		"You may tell the person what you left undone, with talkoot_send to human. Any other tool call stops this turn.", wait)
}

// askedText is what a set of questions asked, for the room.
//
// 🚨 It is never empty. The run's end reads an empty text as "the run asked
// nothing", and it would then send a report, which wakes the member.
func askedText(qs []core.UserQuestion) string {
	parts := make([]string, 0, len(qs))
	for _, q := range qs {
		if t := strings.TrimSpace(q.Question); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "a question with no text"
	}
	return strings.Join(parts, " / ")
}

// hold stops the run's time limit while a card of the run is open, and
// unhold starts it again with the time that was left.
func (rr *routineRun) hold() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.holds++
	if rr.holds == 1 && rr.limit != nil && !rr.limitOver && rr.limit.Stop() {
		rr.held = true
		rr.left = max(rr.left-time.Since(rr.since), 0)
	}
}

func (rr *routineRun) unhold() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.holds--
	// 🚨 Only a timer that hold stopped starts again. One that fired, or that
	// the run's end stopped, stays as it is: a second firing would end a run
	// that is over, or one that the deadline already ended.
	if rr.holds == 0 && rr.held && !rr.limitOver {
		rr.held = false
		rr.since = time.Now()
		rr.limit.Reset(rr.left)
	}
}

// askedAgain reports true one time for a run that asked a person, got no
// answer, and then calls a tool. The caller stops the turn.
//
// 🔑 The run was told to write its last message and to call no tool. A run
// that calls one goes on with work that waits for a person, and that is the
// one thing the deadline must not allow.
func (rr *routineRun) askedAgain() bool {
	return rr.unanswered.Load() != nil && !rr.ended.Load() && rr.wentOn.CompareAndSwap(false, true)
}
