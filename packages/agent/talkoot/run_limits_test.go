package talkoot

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// limited is a task routine of helm with the two limits that part 2 keeps.
func (f *fixture) limited(spend float64, followUps int) Routine {
	r := f.taskOf("helm")
	r.Fields.SpendUSDPerDay, r.Fields.FollowUps = spend, followUps
	return r
}

// follow sets the follow-up of run as the host does: the check, and then the
// line.
func (f *fixture) follow(run Run, reason string) Wake {
	f.t.Helper()
	wk, err := f.router.NewRunWake(run.ID, reason, 10*time.Minute)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.router.RecordWake(wk); err != nil {
		f.t.Fatal(err)
	}
	return wk
}

// 🚨 Each routine has a daily spend limit of its own (decision 0029 rule
// 17). The router counts what the routine's runs spent today, and a routine
// that passed its limit starts no run until the day changes.
func TestARoutinePastItsDailyLimitStartsNoRun(t *testing.T) {
	f := newFixture(t, nil)
	r := f.limited(0.10, 0)
	run, _ := f.start(r)
	if run.SpendCapUSD != 0.10 || run.SpentUSD != 0 {
		t.Fatalf("a run must know its routine's limit and what is spent: %+v", run)
	}
	f.end(run, RunEnd{CostUSD: 0.06, Report: "Done."})
	// Under the limit, so the control: the next run starts.
	run, _ = f.start(r)
	if run.SpentUSD != 0.06 {
		t.Fatalf("the second run must start from what the first spent, got %v", run.SpentUSD)
	}
	f.end(run, RunEnd{CostUSD: 0.05, Report: "Done."})
	if got := f.router.RoutineSpend(r.ID); got < 0.109 || got > 0.111 {
		t.Fatalf("the routine spent 0.11 today, got %v", got)
	}
	_, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-3"})
	if !errors.Is(err, ErrRunSpent) || !strings.Contains(err.Error(), "its runs spent $0.11 today, and its limit is $0.10 a day") {
		t.Fatalf("a routine past its limit must start no run, and say the two amounts, got %v", err)
	}
	// Another routine of the same member has its own count.
	other := f.limited(0.10, 0)
	other.ID = NewProposalID(f.clock.now().Add(time.Second))
	if run, _, err := f.router.StartRun(RunStart{Routine: other, Session: "sess-4"}); err != nil {
		t.Fatalf("the limit of one routine must not hold another: %v", err)
	} else {
		f.end(run, RunEnd{Report: "Done."})
	}
	// The room holds the count, so a restart keeps it.
	f.reopen()
	if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-5"}); !errors.Is(err, ErrRunSpent) {
		t.Fatalf("a restart must keep what the routine spent today, got %v", err)
	}
	// The limit is for one day.
	f.clock.advance(24 * time.Hour)
	if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-6"}); err != nil {
		t.Fatalf("a new day must empty the routine's spend: %v", err)
	}
}

// 🚨 A limit of 0 means that a run may spend nothing. It must not read as no
// limit: a card that shows $0.00 would then hide a routine with no bound.
func TestALimitOfZeroLetsARunSpendNothing(t *testing.T) {
	for _, c := range []struct {
		spent, limit float64
		over         bool
	}{
		{0, 0, false}, {0.01, 0, true}, {0, 1, false}, {0.99, 1, false}, {1, 1, true}, {1.5, 1, true},
	} {
		if got := RoutineSpendOver(c.spent, c.limit); got != c.over {
			t.Errorf("spent %v of %v: over = %v, want %v", c.spent, c.limit, got, c.over)
		}
	}
	f := newFixture(t, nil)
	r := f.limited(0, 0)
	run, _ := f.start(r)
	f.end(run, RunEnd{Report: "Done."})
	run, _ = f.start(r)
	f.end(run, RunEnd{CostUSD: 0.01, Report: "Done."})
	if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-3"}); !errors.Is(err, ErrRunSpent) {
		t.Fatalf("a routine with a limit of 0 that spent a cent must start no run, got %v", err)
	}
}

// 🚨 Decision 0029 rule 11. A run sets one follow-up, the follow-up starts
// one more run of the routine, and the line of runs ends at the card's
// allowance. Without the limit a run could set itself again for good.
func TestALineOfFollowUpsEndsAtTheAllowance(t *testing.T) {
	f := newFixture(t, nil)
	r := f.limited(5, 2)
	first, _ := f.start(r)
	if first.Follow != 0 || first.FollowUps != 2 {
		t.Fatalf("a run at the routine's time is follow-up 0 of the card's allowance: %+v", first)
	}
	w1 := f.follow(first, "Look at the CI run of #12.")
	if w1.Follow != 1 || w1.Routine != r.ID || w1.Member != "helm" || w1.Chain != first.ID {
		t.Fatalf("the wake must name its routine, its member, the run's chain and the next number: %+v", w1)
	}
	if _, err := f.router.NewRunWake(first.ID, "And #13.", time.Minute); !errors.Is(err, ErrNoFollowUp) || !strings.Contains(err.Error(), "its one follow-up") {
		t.Fatalf("a run sets one follow-up, got %v", err)
	}
	f.end(first, RunEnd{Report: "Still at work."})

	second, text, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-2", Wake: &w1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Follow != 1 {
		t.Fatalf("the run that a wake starts has the wake's number, got %d", second.Follow)
	}
	for _, want := range []string{"terva started this run at the time of a follow-up, and no person did.", "follow-up 1 of 2", "> Look at the CI run of #12."} {
		if !strings.Contains(text, want) {
			t.Errorf("the run's text must hold %q, got:\n%s", want, text)
		}
	}
	root := f.envelopes()[len(f.envelopes())-1].Envelope
	if root.Kind != KindRun || root.ReplyTo != w1.ID || root.Chain.Root != root.ID {
		t.Fatalf("the follow-up's root is a new chain that names its wake: %+v", root)
	}
	if open := f.router.OpenWakes([]Wake{w1}); len(open) != 0 {
		t.Fatal("a wake that started its run is closed")
	}
	// The same wake starts no second run.
	f.end(second, RunEnd{Report: "Still at work."})
	if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-x", Wake: &w1}); !errors.Is(err, ErrWakeGone) {
		t.Fatalf("a wake starts one run, got %v", err)
	}

	// The second run set no wake, so the third one comes from a new first.
	second, _ = f.start(r)
	w2 := f.follow(second, "Look again.")
	f.end(second, RunEnd{Report: "Still at work."})
	third, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-3", Wake: &w2})
	if err != nil || third.Follow != 1 {
		t.Fatalf("want follow-up 1 of a new line, got %+v, %v", third, err)
	}
	w3 := f.follow(third, "And again.")
	if w3.Follow != 2 {
		t.Fatalf("the wake of follow-up 1 starts follow-up 2, got %d", w3.Follow)
	}
	f.end(third, RunEnd{Report: "Still at work."})
	// 🚨 The room holds the number. A restart must not give the line a new
	// allowance.
	f.reopen()
	last, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-4", Wake: &w3})
	if err != nil || last.Follow != 2 {
		t.Fatalf("after a restart the run must still be follow-up 2, got %+v, %v", last, err)
	}
	if _, err := f.router.NewRunWake(last.ID, "Once more.", time.Minute); !errors.Is(err, ErrNoFollowUp) || !strings.Contains(err.Error(), "allows 2, and this run is follow-up 2") {
		t.Fatalf("the run at the allowance sets no follow-up, got %v", err)
	}
}

// A routine whose card allows no follow-up sets none.
func TestARoutineWithNoAllowanceSetsNoFollowUp(t *testing.T) {
	f := newFixture(t, nil)
	run, _ := f.start(f.limited(5, 0))
	if _, err := f.router.NewRunWake(run.ID, "Look later.", time.Minute); !errors.Is(err, ErrNoFollowUp) || !strings.Contains(err.Error(), "allows none") {
		t.Fatalf("want a refusal that names the card, got %v", err)
	}
	if _, err := f.router.NewRunWake("01NOSUCHRUN", "Look later.", time.Minute); !errors.Is(err, ErrNoRun) {
		t.Fatalf("a wake of no run must be refused, got %v", err)
	}
}

// 🚨 A follow-up that a person cancelled, or that the host dropped, starts
// no run. A restart must read the same from the room.
func TestAClosedFollowUpStartsNoRun(t *testing.T) {
	for _, how := range []string{"cancelled", "dropped"} {
		t.Run(how, func(t *testing.T) {
			f := newFixture(t, nil)
			r := f.limited(5, 2)
			run, _ := f.start(r)
			wk := f.follow(run, "Look later.")
			f.end(run, RunEnd{Report: "Done."})
			var err error
			if how == "cancelled" {
				err = f.router.CancelWake(wk, "human:sothr")
			} else {
				err = f.router.DropWake(wk, "the routine is paused")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-2", Wake: &wk}); !errors.Is(err, ErrWakeGone) {
				t.Fatalf("a %s follow-up must start no run, got %v", how, err)
			}
			f.reopen()
			if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-2", Wake: &wk}); !errors.Is(err, ErrWakeGone) {
				t.Fatalf("after a restart a %s follow-up must start no run, got %v", how, err)
			}
			if n := len(f.router.runWakes); n != 0 {
				t.Fatalf("the router must forget a closed follow-up, got %d records", n)
			}
		})
	}
}

// 🚨 The host sets the timer between the check and the line. A run that
// ended in between gets no line, so no follow-up outlives a check that no
// longer holds.
func TestAFollowUpOfARunThatEndedIsNotRecorded(t *testing.T) {
	f := newFixture(t, nil)
	r := f.limited(5, 2)
	run, _ := f.start(r)
	wk, err := f.router.NewRunWake(run.ID, "Look later.", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.end(run, RunEnd{Report: "Done."})
	before := len(f.lines())
	if err := f.router.RecordWake(wk); !errors.Is(err, ErrNoRun) {
		t.Fatalf("a follow-up of a run that ended must not be recorded, got %v", err)
	}
	if after := len(f.lines()); after != before {
		t.Fatalf("a refused follow-up wrote %d lines", after-before)
	}
}

// 🚨 A follow-up belongs to its routine. The host's timer names the routine,
// and no seal covers the timer's file, so a wake must not start a run of
// another routine. FireWake is for a member's wake, and it gives a run's
// follow-up no turn in the member's own session.
func TestAFollowUpStartsOnlyARunOfItsOwnRoutine(t *testing.T) {
	f := newFixture(t, nil)
	r := f.limited(5, 2)
	run, _ := f.start(r)
	wk := f.follow(run, "Look later.")
	f.end(run, RunEnd{Report: "Done."})
	other := f.limited(5, 2)
	other.ID = NewProposalID(f.clock.now().Add(time.Second))
	if _, _, err := f.router.StartRun(RunStart{Routine: other, Session: "sess-2", Wake: &wk}); !errors.Is(err, ErrWakeGone) {
		t.Fatalf("a follow-up must not start a run of another routine, got %v", err)
	}
	// The control: its own routine may still use it.
	if open := f.router.OpenWakes([]Wake{wk}); len(open) != 1 {
		t.Fatal("the refused start must leave the wake open")
	}
	sent := len(f.native.to("helm"))
	deliver, err := f.router.FireWake(wk, 0)
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	if got := len(f.native.to("helm")); got != sent {
		t.Fatal("a run's follow-up must give the member's own session no turn")
	}
	if open := f.router.OpenWakes([]Wake{wk}); len(open) != 0 {
		t.Fatal("a follow-up that was fired as a member's wake is dropped")
	}
}

// 🚨 A run's count of what its routine spent is for the day that the run
// began on. The router empties its count at the day's change, and a run that
// works across it books its whole cost to the new day. A run that began
// before midnight must not be stopped for what the routine spent the day
// before.
func TestARunsSpendBeforeItIsForTheDayItBeganOn(t *testing.T) {
	f := newFixture(t, nil)
	r := f.limited(1, 0)
	first, _ := f.start(r)
	f.end(first, RunEnd{CostUSD: 0.9, Report: "Done."})
	run, _ := f.start(r)
	if got := run.SpentAt(f.clock.now().Add(time.Minute)); got != 0.9 {
		t.Fatalf("on the day it began, the run counts what the routine spent before it, got %v", got)
	}
	if got := run.SpentAt(f.clock.now().Add(24 * time.Hour)); got != 0 {
		t.Fatalf("on the next day the routine has spent nothing before this run, got %v", got)
	}
	// The router agrees: the run's whole cost is the new day's.
	f.clock.advance(24 * time.Hour)
	f.end(run, RunEnd{CostUSD: 0.2, Report: "Done."})
	if got := f.router.RoutineSpend(r.ID); got != 0.2 {
		t.Fatalf("a run that ends on the next day books its cost to that day, got %v", got)
	}
}
