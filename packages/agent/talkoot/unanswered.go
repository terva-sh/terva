package talkoot

import (
	"fmt"
	"strings"
	"time"
)

// A question that nobody answered (decision 0029 rule 12). A chain that a
// routine began has no person at its root, so nobody may be there when one of
// its turns asks a person. The host gives such a question a deadline. At the
// deadline the host answers nothing for the person, and the chain ends.
//
//   - A run's question ends the run. EndRun writes a note to the run's member,
//     with the question in it (RunEnd.Unanswered). A note wakes nobody.
//   - A member's question pauses the chain (Unanswered). A paused chain
//     delivers nothing. A person resumes it, as after the hop limit.
//
// 🔑 The member may still tell the person what it left. A send to that person
// alone passes the pause that Unanswered set, and no other pause. Such a send
// stays in the room and wakes nobody, so it moves no work on.

// GuardUnanswered is the guard of a chain that ended at a question.
const GuardUnanswered = "unanswered"

// maxAskedBytes bounds the question that a guard line or a note carries.
const maxAskedBytes = 512

// Unattended reports whether member works in a chain that a routine began,
// and returns the root of that chain.
func (rt *Router) Unattended(member string) (root string, ok bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	root = rt.active[member]
	if cs := rt.chains[root]; cs == nil || cs.routine == "" {
		return "", false
	}
	return root, true
}

// Unanswered ends the chain with this root at a question of member's that
// nobody answered in wait. asked is what the member asked for. It reports
// false, and changes nothing, when no routine began the chain.
//
// 🚨 It also changes nothing when the member works in another chain now. The
// host read the root when the card opened, and the wait can be hours. A
// delivery that the turn read meanwhile moved the member, and its old chain
// is not the one that waits for a person any more.
//
// 🚨 The pause is set before its line is written, as the hop pause is. A line
// that the room refused must not leave the chain at work.
func (rt *Router) Unanswered(member, root, asked string, wait time.Duration) (bool, error) {
	if _, err := rt.turnMember(member); err != nil {
		return false, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if cs := rt.chains[root]; cs == nil || cs.routine == "" || rt.active[member] != root {
		return false, nil
	}
	reason := fmt.Sprintf("%s asked a person, and nobody answered in %s: %s", member, lateText(wait), askedLine(asked))
	rt.pauses.set(chainScope(root), pauseUnanswered, reason)
	return true, rt.room.Append(Line{Type: LineGuard, At: rt.now(), Guard: GuardUnanswered, Action: ActionPaused, Chain: root, Reason: reason})
}

// leftWordLocked reports whether a send may pass a pause: the one pause that
// holds the sender is the chain's unanswered pause, and the send goes to the
// chain's person and to nobody else.
func (rt *Router) leftWordLocked(from, root string, o Outgoing) bool {
	if rt.pauses.reason(talkootScope) != "" || rt.pauses.reason(memberScope(from)) != "" {
		return false
	}
	if kinds := rt.pauses.kinds(chainScope(root)); len(kinds) != 1 || kinds[0] != pauseUnanswered {
		return false
	}
	person := rt.chain(root).replyTo()
	if person == "" || len(o.To) == 0 || o.Kind == KindProposal || o.Kind == KindHandoff {
		return false
	}
	for _, to := range o.To {
		if to != person {
			return false
		}
	}
	return true
}

// noAskedText stands for a question that had no text. What a turn asked is
// for a person to read. Whether it asked is not: a blank text must still end
// the run with a note, and must still pause the chain.
const noAskedText = "a question with no text"

// askedLine makes what a turn asked one bounded line, and never an empty
// one.
func askedLine(asked string) string {
	if line := oneLine(asked, maxAskedBytes); line != "" {
		return line
	}
	return noAskedText
}

// oneLine makes text one line of at most n bytes.
func oneLine(text string, n int) string {
	return cut(strings.Join(strings.Fields(strings.ToValidUTF8(text, "")), " "), n)
}

// unansweredLead opens the note that ends a run at a question.
const unansweredLead = "The run asked a person, and nobody answered before the deadline. "

// IsUnansweredNote reports whether e is the note that a run leaves when it
// ended at a question that nobody answered. The chat bridge tells the person
// who approved the routine about such a note (decision 0029 rule 22).
//
// 🔑 The router writes that note in one place, unansweredNote, and its body
// opens with unansweredLead. A run's other note says that the run failed.
func IsUnansweredNote(e Envelope) bool {
	return e.Kind == KindNote && IsRoutineActor(e.From) && strings.HasPrefix(e.Body, unansweredLead)
}

// The two marks of the note that ends a run at a question. unansweredNote
// writes them, and UnansweredNoteParts reads them.
const (
	unansweredAsked = "terva answers nothing for a person, so the run ended there. It asked: "
	unansweredLeft  = "\nThe last text of the run:\n"
)

// UnansweredNoteParts returns what the run asked and the last text that the
// run left, from a note that IsUnansweredNote accepts. The question is one
// line. A note that the router cut at its limit returns what the cut left.
func UnansweredNoteParts(e Envelope) (asked, left string) {
	body := strings.TrimPrefix(e.Body, unansweredLead+unansweredAsked)
	asked, left, _ = strings.Cut(body, unansweredLeft)
	return strings.TrimSpace(asked), strings.TrimSpace(left)
}

// unansweredNote is the body of the note that ends a run at a question.
func unansweredNote(asked, report string) string {
	var b strings.Builder
	b.WriteString(unansweredLead + unansweredAsked)
	b.WriteString(askedLine(asked))
	if report = strings.TrimSpace(strings.ToValidUTF8(report, "")); report != "" {
		b.WriteString(unansweredLeft)
		b.WriteString(report)
	}
	text := b.String()
	if len(text) > MaxReportBytes {
		text = strings.ToValidUTF8(text[:MaxReportBytes-len(reportCutMark)], "") + reportCutMark
	}
	return text
}
