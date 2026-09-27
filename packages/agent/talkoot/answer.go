package talkoot

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// A person's answer in the room, and the citation that relays it.
//
// A member that carries a person's decision to its team makes a claim, and a
// teammate's claim approves nothing. When a person answers a member's question,
// the workspace records the answer as a sealed room line with its own id. A
// member cites it as answer:<id>. The router resolves each citation against the
// room before it records the envelope, and copies the answer onto the envelope
// (Envelope.Cites). render prints it as a router line outside the quoted body,
// so the recipient reads the person's own words and not the sender's account
// of them.
//
// 🔑 The answer line names no person. The answer verb carries no name, and a
// name that a client supplied would be a claim the daemon cannot check. What
// the line proves is that the person's client answered the member's card,
// through the channel that also answers an approval card.

// answerPrefix starts a reference that cites a person's answer.
const answerPrefix = "answer:"

// Bounds on what one answer line keeps, and on what one envelope cites. The
// ask tool allows eight questions (core.MaxAskQuestions). A front end may send
// more text than a card shows, so these hold whatever it sent: one answer line
// stays near 24 KiB, and a citing envelope near 100 KiB, far inside the room
// reader's 16 MiB line.
const (
	maxAnswered     = 8
	maxChosen       = 8
	maxAnswerText   = 512
	maxChoiceText   = 256
	maxCites        = 4
	maxCitedDisplay = 240
)

// Answered is a person's answer to one question a member asked.
type Answered struct {
	Question string   `json:"question"`
	Chosen   []string `json:"chosen,omitempty"`
	// Omitted counts the choices past the line's bound, so a cited answer
	// never reads as complete when it is not.
	Omitted  int    `json:"omitted,omitempty"`
	Note     string `json:"note,omitempty"`
	Declined bool   `json:"declined,omitempty"`
}

// Citation is a person's answer that an envelope cites, as the router found it
// in the room. The router sets it. A sender cannot, because Outgoing has no
// such field.
type Citation struct {
	Answer  string     `json:"answer"`
	Asker   string     `json:"asker"`
	At      time.Time  `json:"at"`
	Answers []Answered `json:"answers"`
}

// Answer records a person's answers to the questions that member asked, and
// returns the id a member cites as answer:<id>. The workspace calls it when a
// person's client answers the member's question card.
func (rt *Router) Answer(member string, qs []Answered) (string, error) {
	if _, ok := rt.roster.member(member); !ok {
		return "", fmt.Errorf("talkoot: %q is not a member of %s", member, rt.roster.ID)
	}
	if len(qs) == 0 {
		return "", errors.New("talkoot: an answer needs at least one question")
	}
	qs = boundAnswers(qs)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := rt.now()
	id := newID(now)
	if err := rt.room.Append(Line{Type: LineAnswer, At: now, Member: member, Ref: id, Answers: qs}); err != nil {
		return "", err
	}
	rt.answers[id] = Citation{Answer: id, Asker: member, At: now, Answers: qs}
	return id, nil
}

// citesLocked resolves every answer: reference against the answers in the
// room. It refuses a reference that names no answer, so a member cannot cite a
// decision the person never made.
func (rt *Router) citesLocked(refs []string, fix remedy) ([]Citation, error) {
	var out []Citation
	seen := map[string]bool{}
	for _, ref := range refs {
		id, ok := strings.CutPrefix(ref, answerPrefix)
		if !ok || seen[id] {
			continue
		}
		c, ok := rt.answers[id]
		if !ok {
			return nil, fmt.Errorf("talkoot: %s names no answer from the person in %s; %s", ref, rt.roster.ID, fix.missingAnswer())
		}
		if len(out) == maxCites {
			return nil, fmt.Errorf("talkoot: an envelope cites at most %d answers; cite the ones this decision rests on", maxCites)
		}
		seen[id] = true
		out = append(out, c)
	}
	return out, nil
}

// boundAnswers returns a copy of qs inside the answer line's bounds.
func boundAnswers(qs []Answered) []Answered {
	out := make([]Answered, 0, min(len(qs), maxAnswered))
	for _, q := range qs[:min(len(qs), maxAnswered)] {
		a := Answered{Question: cut(q.Question, maxAnswerText), Note: cut(q.Note, maxAnswerText), Declined: q.Declined}
		for _, c := range q.Chosen[:min(len(q.Chosen), maxChosen)] {
			a.Chosen = append(a.Chosen, cut(c, maxChoiceText))
		}
		a.Omitted = q.Omitted + max(0, len(q.Chosen)-maxChosen)
		out = append(out, a)
	}
	return out
}

// cut shortens s to at most n bytes on a rune boundary. A cut text ends in an
// ellipsis, so a clipped condition does not read as the person's whole answer.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	n -= len(mark)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + mark
}

// renderCite writes one router line for each question a citation answers. The
// line names the asker as the author of the question and the person as the
// author of the choice, and it gives the time of the answer, so an old answer
// to a vague question reads as what it is.
//
// 🚨 The question is a member's text, and the choice and the note are free
// text. Each goes through %q, which escapes every quote and line break, so none
// can end the line or print a second router line.
func renderCite(b *strings.Builder, c Citation) {
	for _, a := range c.Answers {
		fmt.Fprintf(b, "Cites the person's answer %s from %s: %s asked %q, and the person ",
			c.Answer, c.At.UTC().Format("2006-01-02 15:04 UTC"), c.Asker, shorten(a.Question))
		switch {
		case a.Declined:
			b.WriteString("declined to answer")
		case len(a.Chosen) == 0:
			b.WriteString("chose none of the options")
		default:
			b.WriteString("chose ")
			for i, ch := range a.Chosen {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%q", shorten(ch))
			}
			if a.Omitted > 0 {
				fmt.Fprintf(b, ", and %d more", a.Omitted)
			}
		}
		if a.Note != "" {
			fmt.Fprintf(b, ", with the note %q", shorten(a.Note))
		}
		b.WriteString(".\n")
	}
}

// shorten bounds one cited text for display.
func shorten(s string) string {
	if utf8.RuneCountInString(s) <= maxCitedDisplay {
		return s
	}
	return string([]rune(s)[:maxCitedDisplay]) + "…"
}
