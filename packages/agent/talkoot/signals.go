package talkoot

import (
	"strings"
	"unicode"
)

// Signals (TKT-01M3NSM0Q). A signal line records something that happened to
// a member: a tool call that failed, a provider retry, or a question or an
// approval that opened and closed. The router keeps no state from them. The
// expression engine rebuilds each member's mood by a replay of the room, so
// every signal it reacts to must be a line (docs/proposals/talkoot-members.md).
//
// A failed turn is no new line. Its turn line carries the failed guard and
// the reason.
//
// 🔑 A reader takes signals from the room, and never from the router calls.
// The live reader and a replay then see the same lines in the same order,
// whichever goroutine the host wrote them from.

// Card kinds on a card line.
const (
	CardPermission = "permission" // an approval of a tool call
	CardQuestion   = "question"   // a question set from ask_user_question
)

// Card outcomes on a card_close line.
const (
	OutcomeApproved  = "approved"  // a person allowed the tool call
	OutcomeDenied    = "denied"    // a person refused the tool call
	OutcomeAnswered  = "answered"  // a person answered the questions
	OutcomeExpired   = "expired"   // no person answered in time
	OutcomeCancelled = "cancelled" // the turn, the worker, or the daemon ended first
)

// maxSignalField bounds a tool's name and a call's id on a signal line. A
// worker reports both, and nothing else bounds them.
const maxSignalField = 128

// ToolFailed records that the tool call id of member returned an error. tool
// is the tool's name, and why is the error, which the host redacts and
// bounds.
func (rt *Router) ToolFailed(member, id, tool, why string) error {
	return rt.signal(member, Line{Type: LineToolError, Ref: id, Tool: tool, Reason: why})
}

// Retried records that member's turn retried its provider request. attempt
// counts the attempt that failed, from 1, and why is the provider's error.
func (rt *Router) Retried(member string, attempt int, why string) error {
	return rt.signal(member, Line{Type: LineRetry, Attempt: attempt, Reason: why})
}

// CardOpened records that a card of kind card, with the id id, opened for
// member. tool names the tool call a permission card asks about.
func (rt *Router) CardOpened(member, card, id, tool string) error {
	return rt.signal(member, Line{Type: LineCardOpen, Card: card, Ref: id, Tool: tool})
}

// CardClosed records how member's card closed.
func (rt *Router) CardClosed(member, card, id, outcome string) error {
	return rt.signal(member, Line{Type: LineCardClose, Card: card, Ref: id, Outcome: outcome})
}

// signal appends a signal line. A member that left the roster still had the
// signal, as it still spent its turn, so any member id passes.
//
// A signal line that cannot be written does not pause the talkoot, as a lost
// turn does. Nothing the router decides rests on it, and the live reader
// never saw it either.
func (rt *Router) signal(member string, l Line) error {
	m, err := rt.turnMember(member)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	l.At = rt.now()
	l.Member = m.ID
	l.Tool, l.Ref = signalField(l.Tool), signalField(l.Ref)
	return rt.room.Append(l)
}

// signalField drops the control characters from s and bounds it.
func signalField(s string) string {
	return cut(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s), maxSignalField)
}
