package workspace

import (
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
)

// storedRoutine writes a task routine of helm that by approved, as an
// approval does, and returns it.
func storedRoutine(t *testing.T, w *Workspace, by string, at time.Time) talkoot.Routine {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	r := talkoot.NewRoutine(talkoot.NewProposalID(at), "helm", ciTask, "every 15m", talkoot.RoutineFields{SpendUSDPerDay: 5}, talkoot.HumanPrefix+by, at)
	if err := talkoot.SaveRoutine(run.dir, r); err != nil {
		t.Fatal(err)
	}
	return r
}

// pausedByTheDaemon pauses a routine as the end of a failed run does.
func pausedByTheDaemon(t *testing.T, w *Workspace, rid, why string) {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	run.routineMu.Lock()
	w.pauseRoutine(run, rid, why)
	run.routineMu.Unlock()
}

// 🚨 TKT-01M7QFVHC. A run that failed pauses its routine, and the routine
// then starts no run until a person resumes it. The note stayed in the room,
// so a person in chat never learned that a routine had stopped. The chat now
// hears it at once, and only the person who approved the routine hears it.
func TestTheChatHearsThatARoutinePausedItself(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "plan", 0)
	now := time.Now()
	theirs := storedRoutine(t, w, "alex", now)
	mine := storedRoutine(t, w, "sothr", now.Add(time.Second))

	// Another person's routine pauses first. It is theirs to resume, and
	// this chat is not theirs.
	pausedByTheDaemon(t, w, theirs.ID, "a run failed: the token of alex expired")
	pausedByTheDaemon(t, w, mine.ID, "a run failed: the provider refused the request")
	got := conn.waitOutbound(t, "the notice of the paused routine", has("the provider refused the request"))
	want := "a routine of helm paused itself: a run failed: the provider refused the request. it starts no run until you resume it in the Talkoot app."
	if !slices.Contains(got, want) {
		t.Fatalf("the chat holds %q, want %q", got, want)
	}
	if slices.ContainsFunc(got, has("the token of alex expired")) {
		t.Fatalf("the chat got the pause of a routine that another person approved: %q", got)
	}
}

// A pause that a person made carries that person's name, and the chat says
// nothing of it. The person knows.
func TestTheChatSaysNothingOfAPauseThatAPersonMade(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "plan", 0)
	now := time.Now()
	byHand := storedRoutine(t, w, "sothr", now)
	byDaemon := storedRoutine(t, w, "sothr", now.Add(time.Second))
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.room.Append(talkoot.RoutineLine(byHand, talkoot.ActionPaused, talkoot.HumanPrefix+"sothr", "a pause by hand", now)); err != nil {
		t.Fatal(err)
	}
	run.flush()
	// The second pause is the positive control: the pump did read the first.
	pausedByTheDaemon(t, w, byDaemon.ID, "a run failed: the positive control")
	got := conn.waitOutbound(t, "the notice of the second pause", has("the positive control"))
	if slices.ContainsFunc(got, has("a pause by hand")) {
		t.Fatalf("the chat got a pause that the person made: %q", got)
	}
}

// 🚨 A run that ended at a question that nobody answered leaves a note. It
// waits for the brief, and it does not interrupt the person. Only the person
// who approved the routine gets it.
func TestTheBriefHoldsARunThatEndedAtAQuestion(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "plan", 0)
	now := time.Now()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	endAtAQuestion := func(r talkoot.Routine, session, asked string) {
		t.Helper()
		var deliver func()
		if err := run.do(func(rt *talkoot.Router) error {
			rr, _, err := rt.StartRun(talkoot.RunStart{Routine: r, Session: session})
			if err != nil {
				return err
			}
			deliver, err = rt.EndRun(rr.ID, talkoot.RunEnd{Unanswered: asked, Report: "I left the revert undone."})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		deliver()
	}
	endAtAQuestion(storedRoutine(t, w, "alex", now), "sess-theirs", "Shall I close the ticket of alex?")
	endAtAQuestion(storedRoutine(t, w, "sothr", now.Add(time.Second)), "sess-mine", "Shall I revert the merge?")

	held := func() []string {
		var out []string
		if v := w.chatOutboundView("crew"); v != nil {
			for _, h := range v.Held {
				out = append(out, h.Text)
			}
		}
		return out
	}
	waitFor(t, "the note in the queue for the brief", func() bool {
		return slices.ContainsFunc(held(), has("Shall I revert the merge?"))
	}, func() string { return "the queue holds: " + strings.Join(held(), " | ") })
	got := held()
	want := "a run of a routine of helm ended at a question that nobody answered: Shall I revert the merge? what the run left: I left the revert undone."
	if len(got) != 1 || got[0] != want {
		t.Fatalf("the queue holds %q, want the one note of the person's own routine: %q", got, want)
	}
	if out := conn.outbound(); slices.ContainsFunc(out, has("Shall I revert the merge?")) {
		t.Fatalf("the note went out before the brief: %q", out)
	}
	// The team's quiet moment does not take it for a last word.
	if lines := w.chatOutboundFor("crew").settled(chatNow()); len(lines) != 0 {
		t.Fatalf("a quiet moment sent the note before the brief: %+v", lines)
	}
	// The brief carries it.
	lines := w.chatOutboundFor("crew").briefCame(chatNow())
	if !slices.ContainsFunc(texts(lines), has("Shall I revert the merge?")) {
		t.Fatalf("the brief must carry the note: %q", texts(lines))
	}
}
