package workspace

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/talkoot"
)

// skippedIntro is why a skipped kickoff's members get plain cards.
const skippedIntro = "the person skipped the introductions"

// introduce asks m to introduce itself in the name of by. A member that
// cannot take a turn gets a plain card from its entry instead, with no model
// call: a worker with no process yet, or a member that a cap or a person
// paused. A non-empty plain writes the card for any member, and names why.
func (w *Workspace) introduce(run *talkootRun, by string, m talkoot.Member, mode talkoot.IntroMode, plain string) (talkoot.Envelope, error) {
	person := strings.TrimPrefix(humanBy(by), talkoot.HumanPrefix)
	var e talkoot.Envelope
	err := run.do(func(rt *talkoot.Router) error {
		why := plain
		switch {
		case why != "":
		case w.memberUnbound(m) != nil:
			why = w.memberUnbound(m).Error()
		default:
			why = pausedWhy(rt, m.ID)
		}
		if why != "" {
			return rt.Card(m.ID, introCard(m, why))
		}
		var err error
		e, err = rt.Introduce(person, m.ID, mode)
		return err
	})
	return e, err
}

// pausedWhy returns why a member cannot take a turn now: a cap it reached, the
// team's budget, or a person's pause. It returns "" for a member that can.
func pausedWhy(rt *talkoot.Router, member string) string {
	for _, s := range rt.Statuses() {
		if s.Member == member && s.Paused != "" {
			return "the member is paused: " + s.Paused
		}
	}
	return ""
}

// introCard is a member's plain introduction, from its roster entry and its
// persona's summary. It stands in for a post in the member's own voice.
func introCard(m talkoot.Member, why string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s, the %s", m.ID, m.Role)
	if m.Title != "" {
		fmt.Fprintf(&b, " (%s)", m.Title)
	}
	b.WriteString(".\n")
	if p, ok := persona.Lookup(m.Persona); ok && m.Persona != "" {
		if p.Summary != "" {
			fmt.Fprintf(&b, "Job: %s\n", p.Summary)
		}
		if len(p.AvoidFor) > 0 {
			fmt.Fprintf(&b, "Will not do: %s\n", strings.Join(p.AvoidFor, "; "))
		}
	}
	posture := m.Posture
	if posture == "" {
		posture = "the default"
	}
	fmt.Fprintf(&b, "Posture: %s.", posture)
	if len(m.Tools) > 0 {
		fmt.Fprintf(&b, " Tools: %s.", strings.Join(m.Tools, ", "))
	}
	if m.Reviewer {
		b.WriteString(" Reviews the work of others.")
	}
	fmt.Fprintf(&b, "\nThis card stands in for an introduction, because %s.", why)
	return b.String()
}

// introduceJoined asks each member an update added to introduce itself, in
// the name of the person who made or approved the update. A failure costs the
// introduction and never the update, so it is reported and not returned.
//
// While the team's kickoff waits, the kickoff introduces every member, so a
// join introduction would be a second one, and its message would wake the
// coordinator before the kickoff. While a kickoff runs here and has not
// reached the coordinator, a member that joins introduces itself with notes
// alone, as the kickoff's members do, and the coordinator waits for its turn.
//
// A kickoff record that does not read leaves the kickoff's state unknown. A
// member that joins then gets a plain card, which wakes nobody and costs
// nothing.
func (w *Workspace) introduceJoined(run *talkootRun, by string, changes []talkoot.MemberChange) {
	plain := ""
	k, ok, err := talkoot.LoadKickoff(run.dir)
	switch {
	case err != nil:
		w.diagf("talkoot %s: %v", run.id, err)
		plain = "the team's kickoff record did not read"
	case ok && k.State == ctrlproto.TalkootKickoffWaiting:
		return
	}
	run.kickMu.Lock()
	defer run.kickMu.Unlock()
	kickoff := run.kicking.Load() && !run.kickLed
	mode := talkoot.IntroJoin
	if kickoff {
		mode = talkoot.IntroKickoff
	}
	for _, c := range changes {
		if c.Before != nil || c.After == nil {
			continue
		}
		e, err := w.introduce(run, by, *c.After, mode, plain)
		if err != nil {
			w.diagf("talkoot %s: could not introduce %s: %v", run.id, c.Member, err)
			continue
		}
		if kickoff && e.ID != "" {
			run.kickWoke = append(run.kickWoke, c.Member)
		}
	}
}
