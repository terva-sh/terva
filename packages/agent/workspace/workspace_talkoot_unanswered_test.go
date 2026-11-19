package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
)

const (
	branchAsk = `{"question":"Which branch is the release?","options":["main","release"]}`
	revertAsk = `{"question":"Shall I revert the merge?","options":["yes","no"]}`
)

// shortWait makes the wait for a person short. Call it before the daemon
// starts, so the daemon is closed before the wait is put back.
func shortWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := questionWait
	questionWait = func() time.Duration { return d }
	t.Cleanup(func() { questionWait = old })
}

func unansweredGuards(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineGuard && l.Guard == talkoot.GuardUnanswered })
}

// expiredCards returns the close of each card that nobody answered in time.
func expiredCards(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineCardClose && l.Outcome == talkoot.OutcomeExpired
	})
}

// 🚨 Criterion 6, for a run. A run asks a person, nobody answers, and the
// run ends at the deadline with a note that holds the question and what the
// run left. The host answers nothing for the person.
func TestARunsQuestionThatNobodyAnswersEndsTheRun(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "I left the third check undone.", callName: "ask_user_question", callArgs: branchAsk}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	before := len(p.rest())
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	closed := expiredCards(t)
	if len(closed) != 1 || closed[0].Member != "helm" || closed[0].Card != talkoot.CardQuestion {
		t.Fatalf("the question's card must close as expired: %+v", closed)
	}
	bodies := p.runs()
	if len(bodies) != 2 {
		t.Fatalf("the run asks, reads the refusal and writes its last text, got %d requests", len(bodies))
	}
	// 🚨 The run reads that nobody answered. It reads no answer.
	for _, want := range []string{"no person answered in", "terva answers nothing for a person", "this run ends here"} {
		if !strings.Contains(bodies[1], want) {
			t.Errorf("the run must read %q after the deadline", want)
		}
	}
	tl := runTurns(t)[0]
	if tl.Guard != talkoot.GuardUnanswered || tl.Interrupted || !strings.Contains(tl.Reason, "Which branch is the release?") {
		t.Fatalf("the run's line must say that nobody answered, and what it asked: %+v", tl)
	}
	notes := runEnds(t, talkoot.KindNote)
	if len(runEnds(t, talkoot.KindReport)) != 0 || len(notes) != 1 {
		t.Fatalf("a run that nobody answered ends with one note and no report, got %d notes", len(notes))
	}
	for _, want := range []string{"It asked: Which branch is the release?", "I left the third check undone."} {
		if !strings.Contains(notes[0].Envelope.Body, want) {
			t.Errorf("the note must hold %q, got:\n%s", want, notes[0].Envelope.Body)
		}
	}
	// 🚨 A note wakes nobody, so the chain ends there. The routine is not
	// paused, because a question with no answer is no failure.
	time.Sleep(100 * time.Millisecond)
	if n := len(p.rest()); n != before {
		t.Fatalf("the member took %d turns after a run that nobody answered", n-before)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("a question with no answer must not pause the routine: %+v", r)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("the inbox must hold no card of a run that ended: %+v", got)
	}
}

// 🚨 A run that goes on after the deadline is stopped at its next tool call.
// "Decline and continue" is what decision 0029 rejected: the run would work
// around a person who is away.
func TestARunThatGoesOnAfterNoAnswerIsStopped(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "unused", callName: "ask_user_question", callArgs: branchAsk, again: true}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	if n := len(p.runs()); n != 2 {
		t.Fatalf("the run is stopped at the tool call after the deadline, and it made %d requests", n)
	}
	if n := len(expiredCards(t)); n != 1 {
		t.Fatalf("the second question must open no card, got %d expired cards", n)
	}
	tl := runTurns(t)[0]
	if tl.Guard != talkoot.GuardUnanswered || tl.Interrupted {
		t.Fatalf("the stop is the end of a run that nobody answered, and no interruption: %+v", tl)
	}
	notes := runEnds(t, talkoot.KindNote)
	if len(notes) != 1 || !strings.Contains(notes[0].Envelope.Body, "It asked: Which branch is the release?") || strings.Contains(notes[0].Envelope.Body, "The last text of the run") {
		t.Fatalf("the note holds the question, and no last text of a run that wrote none: %+v", notes)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("the routine must stay active: %+v", r)
	}
}

// The control of the two tests above. A person answers before the deadline,
// and the run goes on to its report.
func TestARunsQuestionThatAPersonAnswersGoesOn(t *testing.T) {
	p := &runScript{reply: "The release branch passed.", callName: "ask_user_question", callArgs: branchAsk}
	w, seat, clock, _, _ := runCrew(t, p, false)
	if got := questionWait(); got != config.DefaultTalkootQuestionWait {
		t.Fatalf("with no key the wait is the default, got %s", got)
	}
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the run's question", func() bool { return len(inbox(t, w)) == 1 })
	card := inbox(t, w)[0]
	if card.Kind != ctrlproto.TalkootCardAsk || card.Member != "helm" || card.Ask == nil || card.Ask.Question != "Which branch is the release?" {
		t.Fatalf("the run's question is a card of its member: %+v", card)
	}
	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "release"}}); err != nil {
		t.Fatal(err)
	}
	waitRunEnds(t, w, "the end of the run", 1)
	if tl := runTurns(t)[0]; tl.Guard != "" || tl.Interrupted {
		t.Fatalf("a run whose question a person answered ends well: %+v", tl)
	}
	if len(runEnds(t, talkoot.KindReport)) != 1 || len(expiredCards(t)) != 0 {
		t.Fatal("the run reports, and no card expired")
	}
	if bodies := p.runs(); len(bodies) != 2 || !strings.Contains(bodies[1], "release") {
		t.Fatalf("the run must read the person's answer, got %d requests", len(bodies))
	}
}

// 🚨 An approval that nobody gives has the same deadline. Nobody approved
// the call, so it does not run, and the run ends.
func TestARunsApprovalThatNobodyGivesEndsTheRun(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "I could not run the check.", callName: "bash", callArgs: `{"command":"echo hi"}`}
	cwd := talkootHome(t)
	routinesConfig(t, `{"max": 5}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, p.serve)
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: ask\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
		t.Fatal(err)
	}
	seat := helmAfterOneTurn(t, w)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	closed := expiredCards(t)
	if len(closed) != 1 || closed[0].Card != talkoot.CardPermission {
		t.Fatalf("the approval's card must close as expired: %+v", closed)
	}
	bodies := p.runs()
	if len(bodies) != 2 || !strings.Contains(bodies[1], "no person answered in") {
		t.Fatalf("the run must read that nobody answered, got %d requests", len(bodies))
	}
	// 🚨 The command did not run. Its output would be in the next request.
	if strings.Contains(bodies[1], `hi\n`) {
		t.Fatal("a call that nobody approved must not run")
	}
	notes := runEnds(t, talkoot.KindNote)
	if tl := runTurns(t)[0]; tl.Guard != talkoot.GuardUnanswered || len(notes) != 1 ||
		!strings.Contains(notes[0].Envelope.Body, "It asked: an approval of a call of bash") {
		t.Fatalf("the run ends with a note that names the approval: %+v %+v", tl, notes)
	}
}

// 🚨 Criterion 6, for a member. The run reports, its member's turn asks a
// person, and nobody answers. The chain that the routine began is paused at
// the deadline, and the member may still tell the person what it left.
func TestAMembersQuestionInARoutinesChainEndsTheChain(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "The lint job of pull request 12 failed.", memberAsk: revertAsk}
	w, seat, clock, _, _ := runCrew(t, p, false)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the pause of the chain", func() bool { return len(unansweredGuards(t)) == 1 })
	root := runRoots(t)[0].Envelope.ID
	g := unansweredGuards(t)[0]
	if g.Action != talkoot.ActionPaused || g.Chain != root || !strings.Contains(g.Reason, "helm asked a person, and nobody answered in") || !strings.Contains(g.Reason, "Shall I revert the merge?") {
		t.Fatalf("the room must hold the pause of the run's chain, with the member and the question: %+v", g)
	}
	// ⚠️ The pause is in the room before the card's close is. The asker ends
	// the chain first, and its card closes when the asker returns. So the
	// close has a wait of its own. A read at the pause found no close on a
	// loaded runner, in run 7871 of the forge.
	waitTalkoot(t, "the close of the question's card", func() bool { return len(expiredCards(t)) == 1 })
	if closed := expiredCards(t); len(closed) != 1 || closed[0].Member != "helm" || closed[0].Card != talkoot.CardQuestion {
		t.Fatalf("the question's card must close as expired: %+v", closed)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	rest := p.rest()
	if last := rest[len(rest)-1]; !strings.Contains(last, "no person answered in") || !strings.Contains(last, "the chain ends here") {
		t.Fatal("the member must read that nobody answered, and that the chain ends")
	}
	// The pause holds no member: helm is free for work that a person starts.
	if s := memberView(t, w, "crew", "helm").Status; s.Paused != "" {
		t.Fatalf("the pause of a chain holds no member: %+v", s)
	}
	if _, err := seat.Send(talkoot.Outgoing{To: []string{"jev"}, Kind: talkoot.KindMessage, Body: "Revert the merge."}); !errors.Is(err, talkoot.ErrPaused) {
		t.Fatalf("a send to a member in the chain must be refused, got %v", err)
	}
	if _, err := seat.Send(talkoot.Outgoing{To: []string{"human"}, Kind: talkoot.KindMessage, Body: "I left the revert undone."}); err != nil {
		t.Fatalf("a word to the person must pass: %v", err)
	}
}

// memberTurns returns the turn lines of member's own session.
func memberTurns(t *testing.T, member string) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineTurn && l.Member == member && !talkoot.IsRoutineActor(l.Ref)
	})
}

// afterTheRefusal counts the member's requests that came after the result of
// its second tool call.
func afterTheRefusal(p *runScript) int {
	n := 0
	for _, body := range p.rest() {
		if strings.Count(body, `"role":"tool"`) >= 2 {
			n++
		}
	}
	return n
}

// 🚨 A member that goes on after the deadline is stopped at its next tool
// call, as a run is. The pause of the chain refuses a send to a teammate,
// and it refuses nothing that the member does on its own.
func TestAMemberThatGoesOnAfterNoAnswerIsStopped(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "The lint job of pull request 12 failed.", memberAsk: revertAsk, memberThen: "talkoot_roster", memberThenArgs: "{}"}
	w, seat, clock, _, _ := runCrew(t, p, false)
	approved(t, w, seat, ciTask)
	before := len(memberTurns(t, "helm"))
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the pause of the chain", func() bool { return len(unansweredGuards(t)) == 1 })
	waitTalkoot(t, "the end of helm's turn", func() bool { return len(memberTurns(t, "helm")) == before+1 })
	// 🚨 The stop is at the tool call. A turn that went on would send the
	// model the result of that call.
	if n := afterTheRefusal(p); n != 0 {
		t.Fatalf("the member went on for %d requests after the tool call that had to stop it", n)
	}
	// The stop is for that turn. A turn that a person starts works again.
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "I am back. Leave the merge.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's next turn", func() bool { return len(memberTurns(t, "helm")) == before+2 })
	if tl := memberTurns(t, "helm")[before+1]; tl.Interrupted || tl.Guard != "" {
		t.Fatalf("the member's next turn must run as any turn: %+v", tl)
	}
	// 🚨 The mark is for one turn. A mark that stayed would stop the first
	// tool call of every later turn.
	if s := w.existing(memberView(t, w, "crew", "helm").Session); s == nil || s.unanswered.Load() {
		t.Fatal("the next turn of the member must clear the mark of the question")
	}
}

// The control of the test above, and the one call that passes: the member
// tells the person what it left, and its turn goes on to its end.
func TestAMemberMayTellThePersonWhatItLeftAfterNoAnswer(t *testing.T) {
	shortWait(t, 150*time.Millisecond)
	p := &runScript{reply: "The lint job of pull request 12 failed.", memberAsk: revertAsk,
		memberThen: "talkoot_send", memberThenArgs: `{"to":["human"],"kind":"message","body":"I left the revert undone."}`}
	w, seat, clock, _, _ := runCrew(t, p, false)
	approved(t, w, seat, ciTask)
	before := len(memberTurns(t, "helm"))
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the end of helm's turn", func() bool { return len(memberTurns(t, "helm")) == before+1 })
	if n := afterTheRefusal(p); n != 1 {
		t.Fatalf("a word to the person must not stop the member's turn, and it made %d requests after it", n)
	}
	said := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope != nil && l.Envelope.From == "helm" && l.Envelope.Body == "I left the revert undone."
	})
	if len(said) != 1 || len(said[0].Envelope.To) != 1 || said[0].Envelope.To[0] != "human:sothr" {
		t.Fatalf("the room must hold the member's word to the person: %+v", said)
	}
}

// 🚨 A chain that a person began keeps the rule it had. Its question waits
// for as long as the turn does, and no deadline of this kind ends it.
func TestAQuestionInAChainThatAPersonBeganHasNoDeadline(t *testing.T) {
	shortWait(t, 100*time.Millisecond)
	p := &runScript{memberAsk: revertAsk, memberAskOn: "Ask me"}
	w, _, _, _, _ := runCrew(t, p, false)
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "Ask me before you revert.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's question", func() bool { return len(inbox(t, w)) == 1 })
	time.Sleep(500 * time.Millisecond)
	cards := inbox(t, w)
	if len(cards) != 1 || len(expiredCards(t)) != 0 || len(unansweredGuards(t)) != 0 {
		t.Fatalf("a question in a person's chain must stay open past the wait: %d cards, %d expired", len(cards), len(expiredCards(t)))
	}
	if err := w.Answer(t.Context(), cards[0].Session, cards[0].ID, []core.UserAnswer{{Answer: "yes"}}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
}

// 🚨 A person who types into a member's session is at the keyboard, though
// the member's chain is one that a routine began. That turn's question has
// no deadline either, and it ends no chain.
func TestAQuestionInATurnThatAPersonTypedHasNoDeadline(t *testing.T) {
	shortWait(t, 100*time.Millisecond)
	p := &runScript{reply: "The lint job of pull request 12 failed.", memberAsk: revertAsk, memberAskOn: "Ask me"}
	w, seat, clock, _, _ := runCrew(t, p, false)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	// The report's turn ends, and helm then works in the run's chain.
	waitRunEnds(t, w, "the end of the run", 1)
	waitTalkoot(t, "helm's turn on the report", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns == 3 && !m.Status.Working
	})
	root := runRoots(t)[0].Envelope.ID
	var active string
	if err := seat.b.run.do(func(rt *talkoot.Router) error { active, _ = rt.Unattended("helm"); return nil }); err != nil || active != root {
		t.Fatalf("helm must work in the chain of the run, got %q %v", active, err)
	}
	sess := memberView(t, w, "crew", "helm").Session
	if err := w.Prompt(t.Context(), sess, ctrlproto.PromptParams{Text: "Ask me before you revert."}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's question", func() bool { return len(inbox(t, w)) == 1 })
	time.Sleep(500 * time.Millisecond)
	cards := inbox(t, w)
	if len(cards) != 1 || len(expiredCards(t)) != 0 || len(unansweredGuards(t)) != 0 {
		t.Fatalf("a question in a turn that a person typed must stay open past the wait: %d cards, %d expired", len(cards), len(expiredCards(t)))
	}
	if err := w.Answer(t.Context(), cards[0].Session, cards[0].ID, []core.UserAnswer{{Answer: "yes"}}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
}

// 🚨 What a card asked is never an empty text. The end of a run reads an
// empty text as a run that asked nothing.
func TestWhatACardAskedIsNeverEmpty(t *testing.T) {
	for _, qs := range [][]core.UserQuestion{nil, {{Question: ""}}, {{Question: "  "}, {Question: "\n"}}} {
		if got := askedText(qs); strings.TrimSpace(got) == "" {
			t.Fatalf("askedText(%+v) is empty", qs)
		}
	}
	if got := askedText([]core.UserQuestion{{Question: " Which port? "}, {Question: ""}, {Question: "Which host?"}}); got != "Which port? / Which host?" {
		t.Fatalf("got %q", got)
	}
}

// A turn is unattended only when deliveries alone fed it. A turn that a
// person typed into is attended, whatever chain its member holds.
func TestATurnThatAPersonTypedIntoIsAttended(t *testing.T) {
	var r talkootReads
	r.taken.Store(3)
	r.users.Store(7)
	r.turnTaken.Store(3)
	r.turnUsers.Store(7)
	if r.deliveredOnly() {
		t.Fatal("a turn that read nothing yet is not a delivery's turn")
	}
	r.users.Add(1)
	if r.deliveredOnly() {
		t.Fatal("a turn that a person typed, with no delivery, is attended")
	}
	r.taken.Add(1)
	if !r.deliveredOnly() {
		t.Fatal("a turn that one delivery opened is a delivery's turn")
	}
	r.users.Add(1)
	if r.deliveredOnly() {
		t.Fatal("a person who types into a delivery's turn makes it attended")
	}
	r.taken.Add(1)
	if !r.deliveredOnly() {
		t.Fatal("a second delivery keeps the turn a delivery's turn")
	}
}

// 🚨 A run's time limit does not count the wait for a person. The limit's
// timer stops while a card of the run is open, and goes on with what was
// left. A timer that fired, or that the run's end stopped, never starts
// again.
func TestARunsTimeLimitDoesNotCountTheWaitForAPerson(t *testing.T) {
	arm := func(span time.Duration) (*routineRun, chan struct{}) {
		rr, fired := &routineRun{}, make(chan struct{}, 4)
		rr.mu.Lock()
		rr.span, rr.left, rr.since = span, span, time.Now()
		rr.limit = time.AfterFunc(span, func() { fired <- struct{}{} })
		rr.mu.Unlock()
		return rr, fired
	}
	fires := func(fired chan struct{}, within time.Duration) bool {
		select {
		case <-fired:
			return true
		case <-time.After(within):
			return false
		}
	}

	rr, fired := arm(150 * time.Millisecond)
	rr.hold()
	rr.hold() // two cards at one time
	if fires(fired, 400*time.Millisecond) {
		t.Fatal("the limit fired while a card of the run was open")
	}
	rr.unhold()
	if fires(fired, 300*time.Millisecond) {
		t.Fatal("the limit fired while one of two cards was still open")
	}
	rr.unhold()
	if !fires(fired, 5*time.Second) {
		t.Fatal("the limit must go on when the last card closes")
	}
	// The limit fired. A card that opens and closes after that starts nothing.
	rr.hold()
	rr.unhold()
	if fires(fired, 400*time.Millisecond) {
		t.Fatal("a limit that fired must not fire again")
	}

	// The run's end stops the limit while a card is open. The close of the
	// card must not start it again.
	rr, fired = arm(150 * time.Millisecond)
	rr.hold()
	rr.stopLimit()
	rr.unhold()
	if fires(fired, 400*time.Millisecond) {
		t.Fatal("the limit of a run that ended must not start again")
	}

	// The control: with no card the limit fires at its time.
	_, fired = arm(100 * time.Millisecond)
	if !fires(fired, 5*time.Second) {
		t.Fatal("the limit must fire with no card open")
	}
}
