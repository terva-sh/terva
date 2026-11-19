package tools

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
)

type fakeRoutiner struct {
	ask   talkoot.RoutineAsk
	why   string
	calls int
	diffs []talkoot.RoutineDiff
	err   error
}

func (r *fakeRoutiner) ProposeRoutine(ask talkoot.RoutineAsk, why string) (TalkootRoutineReceipt, error) {
	r.calls++
	r.ask, r.why = ask, why
	if r.err != nil {
		return TalkootRoutineReceipt{}, r.err
	}
	return TalkootRoutineReceipt{Proposal: talkoot.Proposal{ID: "01ROUTINE", Routine: &ask}, Schedule: "every 15m", Diffs: r.diffs}, nil
}

func TestTalkootRoutineHandsTheAskToTheSeat(t *testing.T) {
	seat := &fakeRoutiner{}
	got, err := talkootText(t, &TalkootRoutineTool{Seat: seat}, `{"kind":"task","schedule":"every 15m","prompt":"Check the CI run of each open pull request.","why":"The person asked for a watch on CI."}`)
	if err != nil {
		t.Fatal(err)
	}
	a := seat.ask
	if a.Kind != talkoot.RoutineTask || a.Schedule != "every 15m" || a.Prompt != "Check the CI run of each open pull request." || seat.why != "The person asked for a watch on CI." {
		t.Fatalf("the seat got %+v and %q", a, seat.why)
	}
	// 🚨 A limit that the member left out must reach the seat as "the
	// default", and never as a zero that the member did not ask for.
	if a.EndAfterRuns != nil || a.EndAfterDays != nil || a.SpendUSDPerDay != nil || a.ReviewsPerDay != nil || a.FollowUps != nil || a.CatchUp != nil {
		t.Fatalf("a limit the member left out must stay unset: %+v", a)
	}
	for _, want := range []string{"01ROUTINE", "a task, every 15m", "waits for the person's decision", "Every limit has the default of its kind."} {
		if !strings.Contains(got, want) {
			t.Errorf("the result must say %q, got %q", want, got)
		}
	}
}

func TestTalkootRoutineSaysWhichLimitsTheCardShows(t *testing.T) {
	seat := &fakeRoutiner{diffs: []talkoot.RoutineDiff{{Field: talkoot.FieldSpendUSDPerDay, Value: "25.00", Default: "5.00"}, {Field: talkoot.FieldCatchUp, Value: "false", Default: "true"}}}
	got, err := talkootText(t, &TalkootRoutineTool{Seat: seat}, `{"kind":"watcher","schedule":"every 5m","command":"tea pulls ls","why":"Watch the queue.","spend_usd_per_day":25,"catch_up":false,"follow_ups":0}`)
	if err != nil {
		t.Fatal(err)
	}
	a := seat.ask
	if a.Command != "tea pulls ls" || a.SpendUSDPerDay == nil || *a.SpendUSDPerDay != 25 || a.CatchUp == nil || *a.CatchUp {
		t.Fatalf("the seat got %+v", a)
	}
	// A zero that the member wrote is a value, and not "the default".
	if a.FollowUps == nil || *a.FollowUps != 0 {
		t.Fatalf("a limit the member set to zero must reach the seat as zero: %+v", a.FollowUps)
	}
	for _, want := range []string{"spend_usd_per_day 25.00 (default 5.00).", "catch_up false (default true)."} {
		if !strings.Contains(got, want) {
			t.Errorf("the result must say %q, got %q", want, got)
		}
	}
}

func TestTalkootRoutineRefusesABadCallBeforeTheSeat(t *testing.T) {
	seat := &fakeRoutiner{}
	for _, args := range []string{
		`{"kind":"task","schedule":"every 15m","prompt":"Check it."}`,
		`{"kind":"task","schedule":"every 15m","prompt":"Check it.","why":"  "}`,
		`{"kind":"task","schedule":"every 15m","prompt":"Check it.","why":"x","follow_ups":"many"}`,
		`not json`,
	} {
		if _, err := talkootText(t, &TalkootRoutineTool{Seat: seat}, args); err == nil {
			t.Errorf("%s must be refused", args)
		}
	}
	if seat.calls != 0 {
		t.Errorf("a bad call must not reach the seat, got %d calls", seat.calls)
	}
	if _, err := talkootText(t, &TalkootRoutineTool{}, `{"kind":"task","schedule":"every 15m","prompt":"x","why":"x"}`); err == nil {
		t.Error("a tool with no seat asked for a routine")
	}
}

// The seat names the person's cap in its refusal, and the model must read
// those words as the seat wrote them.
func TestTalkootRoutineReturnsTheSeatsRefusal(t *testing.T) {
	seat := &fakeRoutiner{err: errors.New("the team has 4 routines, and the limit is 4 (talkoot_routines.max)")}
	_, err := talkootText(t, &TalkootRoutineTool{Seat: seat}, `{"kind":"task","schedule":"every 15m","prompt":"x","why":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "the limit is 4 (talkoot_routines.max)") {
		t.Fatalf("want the seat's refusal, got %v", err)
	}
}

// 🚨 The MCP bridge serves TalkootToolDefs to an external member, and decision
// 0023 lists what the bridge may serve. A routine's ask is not on that list.
func TestTalkootRoutineIsNotAmongTheBridgedTools(t *testing.T) {
	name := (&TalkootRoutineTool{}).Name()
	for _, d := range TalkootToolDefs() {
		if d.Name == name {
			t.Fatal("talkoot_routine is in TalkootToolDefs, which the MCP bridge serves")
		}
	}
	if slices.Contains(talkoot.BridgeTools, name) {
		t.Fatal("talkoot_routine is in talkoot.BridgeTools, and decision 0023 does not list it")
	}
	// A roster's tools list narrows it, as it narrows any tool of a job.
	if slices.Contains(talkoot.SeatTools, name) {
		t.Fatal("talkoot_routine is a seat tool, so a member's tools list cannot remove it")
	}
}
