package talkoot

import (
	"errors"
	"strings"
	"testing"
)

// ownCheckout gives gage, a native specialist, a worktree of its own.
func ownCheckout(r *Roster, _ *Limits) {
	for i := range r.Members {
		if r.Members[i].ID == "gage" {
			r.Members[i].Workspace = WorkspaceWorktree
		}
	}
}

// 🚨 TKT-01M7Q6SX. A run takes no working slot. For a member with a checkout
// of its own, nothing else kept a run apart from a second run of the member,
// and two turns in one checkout undo each other's edits.
func TestAMemberWithItsOwnCheckoutHasOneRunAtATime(t *testing.T) {
	f := newFixture(t, ownCheckout)
	first, _ := f.start(f.taskOf("gage"))
	other := f.taskOf("gage")
	_, _, err := f.router.StartRun(RunStart{Routine: other, Session: "sess-2"})
	if !errors.Is(err, ErrRunMemberAtWork) || !strings.Contains(err.Error(), "another run of gage is still at work in its checkout") {
		t.Fatalf("a second run in the member's own checkout must be refused, got %v", err)
	}
	f.end(first, RunEnd{Interrupted: true})
	if _, _, err := f.router.StartRun(RunStart{Routine: other, Session: "sess-3"}); err != nil {
		t.Fatalf("the checkout is free after the run's end: %v", err)
	}
}

// A run starts in no checkout that its member works in. The member's turn
// holds a working slot from its delivery to the turn's end.
func TestARunWaitsForItsMembersTurnInItsOwnCheckout(t *testing.T) {
	f := newFixture(t, ownCheckout)
	f.post("gage")
	r := f.taskOf("gage")
	_, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-1"})
	if !errors.Is(err, ErrRunMemberAtWork) || !strings.Contains(err.Error(), "gage is at work in its checkout") {
		t.Fatalf("a run must not start while its member works in its own checkout, got %v", err)
	}
	if err := f.router.TurnEnded("gage", 0.1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.router.StartRun(RunStart{Routine: r, Session: "sess-2"}); err != nil {
		t.Fatalf("the run starts after the member's turn: %v", err)
	}
}

// 🚨 A delivery to the member waits for the run's end, and the room says so.
// The run's report goes first, and what waited goes after it, one time.
func TestADeliveryWaitsForTheRunInItsMembersOwnCheckout(t *testing.T) {
	f := newFixture(t, ownCheckout)
	run, _ := f.start(f.taskOf("gage"))
	post := f.post("gage")
	if got := f.native.to("gage"); len(got) != 0 {
		t.Fatalf("a delivery reached the member while a run worked in its checkout: %q", got)
	}
	queued := f.guard(GuardWorking)
	if len(queued) != 1 || queued[0].Action != ActionQueued || queued[0].Ref != post.ID ||
		queued[0].Reason != "a run of a routine of gage works in its checkout" {
		t.Fatalf("the room must say why the delivery waits: %+v", queued)
	}
	// A second envelope waits too, and a release for another cause holds
	// both again with no second line for either.
	f.post("gage")
	f.resume("")
	if got := f.native.to("gage"); len(got) != 0 {
		t.Fatalf("a release sent a delivery into the checkout of a run: %q", got)
	}
	if n := len(f.guard(GuardWorking)); n != 2 {
		t.Fatalf("each held delivery has one line, got %d", n)
	}
	f.end(run, RunEnd{Report: "Pull request 13 waits for a review."})
	got := f.native.to("gage")
	if len(got) != 3 || !strings.Contains(got[0], "Pull request 13 waits for a review.") ||
		!strings.Contains(got[1], "Plan the lake schema.") || !strings.Contains(got[2], "Plan the lake schema.") {
		t.Fatalf("the report goes first and each held delivery follows one time: %q", got)
	}
}

// A run that was stopped sends no report. What it held still goes out.
func TestAnInterruptedRunReleasesWhatItHeld(t *testing.T) {
	f := newFixture(t, ownCheckout)
	run, _ := f.start(f.taskOf("gage"))
	f.post("gage")
	f.end(run, RunEnd{Interrupted: true})
	if got := f.native.to("gage"); len(got) != 1 || !strings.Contains(got[0], "Plan the lake schema.") {
		t.Fatalf("the held delivery goes out at the end of an interrupted run: %q", got)
	}
}

// A restart keeps the run open, so it keeps the hold. The host ends a run
// that the last daemon left, and that end releases the delivery.
func TestARestartKeepsTheHoldOfAnOpenRun(t *testing.T) {
	f := newFixture(t, ownCheckout)
	run, _ := f.start(f.taskOf("gage"))
	f.post("gage")
	f.reopen()
	f.router.Release()
	if got := f.native.to("gage"); len(got) != 0 {
		t.Fatalf("a restart sent the delivery into the checkout of an open run: %q", got)
	}
	f.end(run, RunEnd{Failed: "the daemon stopped while the run worked"})
	got := f.native.to("gage")
	if len(got) != 1 || !strings.Contains(got[0], "Plan the lake schema.") {
		t.Fatalf("the delivery goes out one time after the run's end: %q", got)
	}
}

// The rule is for a checkout that a member has for itself. A member in the
// shared checkout works beside every other member already, so its runs and
// its deliveries are as they were.
func TestAMemberInTheSharedCheckoutIsNotHeldByItsRun(t *testing.T) {
	f := newFixture(t, nil)
	run, _ := f.start(f.taskOf("gage"))
	if _, _, err := f.router.StartRun(RunStart{Routine: f.taskOf("gage"), Session: "sess-2"}); err != nil {
		t.Fatalf("a second routine of a member in the shared checkout starts its run: %v", err)
	}
	f.post("gage")
	if got := f.native.to("gage"); len(got) != 1 {
		t.Fatalf("a delivery to a member in the shared checkout does not wait for its run: %q", got)
	}
	if n := len(f.guard(GuardWorking)); n != 0 {
		t.Fatalf("nothing is queued for a member in the shared checkout: %d lines", n)
	}
	f.end(run, RunEnd{Interrupted: true})
}
