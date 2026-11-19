package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// The review count of a chain that no person began (decision 0029 rule 17,
// TKT-01M7N39E0 criterion 4).
//
// 🔑 These tests ask for a review and then read what ran. A program named
// just stands first on PATH, and it writes each call to a file. A test that
// read only the text of a refusal would pass against a count that refuses
// and lets the command run.

const (
	reviewDeniedMark = "denied by the review limit of a routine"
	reviewPattern    = `{"max": 5, "review_commands": ["^just pr-review\\b"]}`
)

// reviewCalls are the review requests that ran, as the program wrote them.
func reviewCalls(t *testing.T, f forge) []string {
	t.Helper()
	log, _ := os.ReadFile(f.log)
	var out []string
	for _, call := range strings.Split(string(log), "\n") {
		if strings.HasPrefix(call, "just pr-review ") {
			out = append(out, call)
		}
	}
	return out
}

// routineView is the one routine of the crew as its list shows it.
func routineView(t *testing.T, w *Workspace) (*ctrlproto.TalkootRoutines, ctrlproto.TalkootRoutine) {
	t.Helper()
	v := w.talkootRoutinesView("crew")
	if v == nil || len(v.Routines) != 1 {
		t.Fatalf("want one routine in the list, got %+v", v)
	}
	return v, v.Routines[0]
}

func reviewLines(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineReview })
}

// 🚨 Criterion 4. A task has two review requests a day. A run asks for two,
// and both run. The third is refused and does not run. A request in a loop
// has no count, so it is refused and does not run either.
func TestEachReviewRequestOfARunCountsAgainstItsRoutine(t *testing.T) {
	p := &runScript{reply: "Pull request 14 waits for a review.", seq: []string{
		// Three requests in one line against a limit of two: the line is
		// refused whole, and none of the three runs.
		"just pr-review 7 && just pr-review 8 && just pr-review 9",
		"just pr-review 12",
		"for n in 15 16; do just pr-review $n; done",
		"timeout 100 just pr-review 13",
		"just pr-review 14",
		"just pr-status 14",
	}}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	routinesConfig(t, reviewPattern)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	if got, want := reviewCalls(t, f), []string{"just pr-review 12", "just pr-review 13"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the review requests that ran are %q, want %q", got, want)
	}
	// The command after the refusals still runs: the count ends no run.
	if log, _ := os.ReadFile(f.log); !strings.Contains(string(log), "just pr-status 14") {
		t.Fatalf("the command after the refused request did not run: %q", log)
	}
	bodies := p.runs()
	last := bodies[len(bodies)-1]
	if n := strings.Count(last, reviewDeniedMark); n != 3 {
		t.Fatalf("the run read %d refusals of the review count, want 3", n)
	}
	// Each refusal says what the daemon found and what to do next.
	for _, want := range []string{
		"terva cannot count the review requests of this command",
		"Ask for each review in a command of its own",
		"its chains asked for 0 today, and its limit is 2 a day. This command asks for 3 reviews.",
		"its chains asked for 2 today, and its limit is 2 a day. Do not try another route.",
		"Say in your last message which pull request waits for a review",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("the run did not read %q", want)
		}
	}
	// The room holds each request that counted, under the routine.
	lines := reviewLines(t)
	if len(lines) != 2 {
		t.Fatalf("the room holds %d review lines, want 2: %+v", len(lines), lines)
	}
	for _, l := range lines {
		if l.Member != "helm" || l.Count != 1 || l.Ref != talkoot.RoutinePrefix+rid || l.Chain == "" {
			t.Fatalf("a review line must name the member, the routine and the chain: %+v", l)
		}
		// A client reads the same from the room.
		if wl := wireTalkootLine(l); wl.Type != talkoot.LineReview || wl.Count != 1 || wl.Member != "helm" || wl.Ref != l.Ref || wl.Chain != l.Chain {
			t.Fatalf("the wire form of a review line must keep its count: %+v", wl)
		}
	}
	// The list of routines shows the count, and says that the daemon counts.
	v, r := routineView(t, w)
	if !v.ReviewsCounted || r.ReviewsToday != 2 || r.Fields.ReviewsPerDay != 2 {
		t.Fatalf("the list must show 2 of 2 reviews, and that they are counted: counted=%v today=%d limit=%d", v.ReviewsCounted, r.ReviewsToday, r.Fields.ReviewsPerDay)
	}
}

// 🚨 The member that reads the run's report is in the chain that the routine
// began. Its request counts against the same routine, so a run cannot leave
// its limit behind by a report that asks the member to go on.
func TestAMemberInARoutinesChainAsksUnderTheSameCount(t *testing.T) {
	p := &runScript{reply: "Pull request 14 waits for a review.", seq: []string{"just pr-review 12", "just pr-review 13"}}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	routinesConfig(t, reviewPattern)
	p.set(func() { p.memberBash, p.memberBashOn = `{"command":"just pr-review 14"}`, reportMark })
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "helm to read the refusal", func() bool {
		rest := p.rest()
		return len(rest) > 0 && strings.Contains(rest[len(rest)-1], reviewDeniedMark)
	})
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	if got, want := reviewCalls(t, f), []string{"just pr-review 12", "just pr-review 13"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the review requests that ran are %q, want %q", got, want)
	}
	if _, r := routineView(t, w); r.ReviewsToday != 2 {
		t.Fatalf("the refused request must count nothing, got %d", r.ReviewsToday)
	}
}

// 🚨 The proof that the tests above can see a request run, and the other half
// of the rule: a request in a chain that a person began counts nothing. The
// same member asks three times against a limit of two, and each one runs.
func TestAReviewRequestInAChainThatAPersonBeganCountsNothing(t *testing.T) {
	p := &runScript{reply: "unused"}
	w, seat, _, f := mergeCrew(t, p, "workspace")
	routinesConfig(t, reviewPattern)
	approved(t, w, seat, ciTask)
	for i := range 3 {
		on := fmt.Sprintf("Ask for review %d.", i)
		p.set(func() {
			p.memberBash, p.memberBashOn = fmt.Sprintf(`{"command":"just pr-review %d"}`, 20+i), on
		})
		tell(t, w, on)
		if rest := p.rest(); strings.Contains(rest[len(rest)-1], reviewDeniedMark) {
			t.Fatalf("request %d was refused in a chain that a person began", i)
		}
	}
	if got, want := reviewCalls(t, f), []string{"just pr-review 20", "just pr-review 21", "just pr-review 22"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the review requests that ran are %q, want %q", got, want)
	}
	if _, r := routineView(t, w); r.ReviewsToday != 0 || len(reviewLines(t)) != 0 {
		t.Fatalf("a chain that a person began must count nothing, got %d and %d lines", r.ReviewsToday, len(reviewLines(t)))
	}
}

// 🚨 With no pattern the daemon counts nothing, and a limit that nothing
// counts must not read as a limit. The list says that reviews are not
// counted, and the card of a new routine says it too.
func TestWithNoPatternNothingCountsAndTheListSaysSo(t *testing.T) {
	p := &runScript{reply: "Done.", seq: []string{"just pr-review 12", "just pr-review 13", "just pr-review 14"}}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	if got := reviewCalls(t, f); len(got) != 3 {
		t.Fatalf("with no pattern each request runs, got %q", got)
	}
	v, r := routineView(t, w)
	if v.ReviewsCounted || r.ReviewsToday != 0 {
		t.Fatalf("the list must say that nothing counts a review: counted=%v today=%d", v.ReviewsCounted, r.ReviewsToday)
	}
	// The card of one more routine is read again at each look, as the list
	// is.
	pr, err := seat.ProposeRoutine(talkoot.RoutineAsk{Kind: talkoot.RoutineTask, Schedule: "every 30m", Prompt: "Read the open tickets."}, "One more routine.")
	if err != nil {
		t.Fatal(err)
	}
	card := func() *ctrlproto.TalkootRoutineAsk { return wireRoutineAsk(pr.Proposal) }
	if ask := card(); ask.ReviewsCounted {
		t.Fatal("the card must say that nothing counts a review")
	}
	routinesConfig(t, reviewPattern)
	if v, _ := routineView(t, w); !v.ReviewsCounted {
		t.Fatal("the list must say that reviews are counted, with the pattern set")
	}
	if ask := card(); !ask.ReviewsCounted {
		t.Fatal("the card must say that reviews are counted, with the pattern set")
	}
}

// 🚨 A request that the daemon cannot count does not run. Here the record of
// the routine is gone while its run is at work, so the daemon has no limit
// to count against.
func TestAReviewRequestThatTheDaemonCannotCountDoesNotRun(t *testing.T) {
	// The scripted provider holds a run's request after its first command.
	// So the first command asks for no review, and the record is gone
	// before the request that follows it.
	p := &runScript{reply: "Done.", seq: []string{"just pr-status 1", "just pr-review 12", "just pr-status 12"}}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	release := p.held(t)
	routinesConfig(t, reviewPattern)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(run.dir, talkoot.RoutinesDir, rid+".json")); err != nil {
		t.Fatal(err)
	}
	release()
	waitTalkoot(t, "the run to read the refusal", func() bool {
		bodies := p.runs()
		return len(bodies) > 0 && strings.Contains(bodies[len(bodies)-1], reviewDeniedMark)
	})
	bodies := p.runs()
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "terva could not count this review request") {
		t.Fatalf("the refusal must say that the daemon could not count: %s", last)
	}
	waitTalkoot(t, "the command after the refusal", func() bool {
		log, _ := os.ReadFile(f.log)
		return strings.Contains(string(log), "just pr-status 12")
	})
	if got := reviewCalls(t, f); len(got) != 0 {
		t.Fatalf("a request that the daemon could not count ran: %q", got)
	}
	if n := len(reviewLines(t)); n != 0 {
		t.Fatalf("the room holds %d review lines for a request that did not run", n)
	}
}
