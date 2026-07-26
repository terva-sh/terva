package core

import (
	"context"
	"strings"
	"testing"
)

// TW-028, rung 2. The fixture is the Wave 3 archiving session
// (f35bb34caca0a141/20260726-060056-87df6b73.jsonl): one email_move signature
// failed 19 times, the detector nudged once per turn, and in the turn where the
// nudge was ignored TEN more identical calls followed it with nothing further
// said. The ladder terminated at rung 1 for any deployment without an
// escalation target — which is every deployment until someone configures one.
//
// These pin that the dead path now speaks, that it speaks exactly once, and
// that a configured escalation still owns the rung it always did.

// churnLoop drives n identical failing calls, varying only the argument (the
// real loop's placeholder churn) so this rides the churn axis.
func churnLoop(a *Agent, n int) {
	for i := 0; i < n; i++ {
		a.stall.observe(
			call("m", "email_move", `{"ids":["placeholder"]}`),
			result("m", "pass exactly one of ids, selection, or receipt", true),
		)
	}
}

// collectStalls drains the events an agent's sink emits into a slice of records.
func collectStalls(evs *[]StallRecord) func(AgentEvent) {
	return func(ev AgentEvent) {
		if s, ok := ev.(EvStall); ok {
			*evs = append(*evs, s.StallRecord)
		}
	}
}

// The default deployment: detection on, no Escalator bound. Rung 3 has nothing
// to escalate to, so rung 2 is the whole remaining ladder.
func TestStallHoldOffSpeaksWhenEscalationCannotAct(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	var got []StallRecord
	sink := collectStalls(&got)

	churnLoop(a, 5) // threshold 3 nudges, watermark 5 raises the request
	if _, ok := a.stall.escalation(); !ok {
		t.Fatal("precondition: five identical failures should raise the escalation request")
	}
	if a.maybeEscalate(context.Background(), sink) {
		t.Fatal("a hold-off must never end the turn — only a user-chosen stop does that")
	}

	nudge := a.stall.nudge()
	if nudge == "" {
		t.Fatal("with nothing to escalate to, rung 2 must speak rather than leave the loop unremarked")
	}
	if !strings.Contains(nudge, "email_move") || !strings.Contains(nudge, "5") {
		t.Errorf("the hold-off should name the tool and the run length:\n%s", nudge)
	}
	// Different prose from rung 1: repeating the first nudge verbatim would
	// itself be a loop, and the gentler reading has already been tried.
	if nudge == stallNudge("email_move", 5, "pass exactly one of ids, selection, or receipt") {
		t.Error("rung 2 must not be rung 1's text repeated")
	}
	if len(got) != 1 || got[0].Rung != 2 {
		t.Fatalf("rung 2 must be recorded as rung 2 so a reader can tell it apart, got %+v", got)
	}
	if got[0].Tool != "email_move" || got[0].Axis != stallAxisChurn {
		t.Errorf("the record should carry the loop's tool and axis, got %+v", got[0])
	}
}

// Once per turn, like every other rung. markEscalated is what bounds it; a
// hold-off that re-fired on each further call would be the noise this whole
// design is trying to avoid.
func TestStallHoldOffSpeaksOnlyOncePerTurn(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	var got []StallRecord
	sink := collectStalls(&got)

	churnLoop(a, 5)
	a.maybeEscalate(context.Background(), sink)
	a.stall.clearNudge()

	churnLoop(a, 5) // ten more identical calls, exactly the turn-2 shape
	a.maybeEscalate(context.Background(), sink)
	if n := a.stall.nudge(); n != "" {
		t.Errorf("rung 2 fired twice in one turn:\n%s", n)
	}
	if len(got) != 1 {
		t.Errorf("one hold-off per turn, got %d records: %+v", len(got), got)
	}
}

// A loop that never reaches the watermark is rung 1's business alone. This is
// the "models that heed the first nudge see no change" criterion: three
// failures nudge, and nothing further happens.
func TestStallHoldOffStaysQuietBelowTheWatermark(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	var got []StallRecord
	sink := collectStalls(&got)

	churnLoop(a, 3)
	a.stall.clearNudge() // rung 1's nudge has ridden its turn
	if a.maybeEscalate(context.Background(), sink) {
		t.Fatal("nothing should stop the turn here")
	}
	if n := a.stall.nudge(); n != "" {
		t.Errorf("three failures is rung 1 only; rung 2 must stay quiet:\n%s", n)
	}
	if len(got) != 0 {
		t.Errorf("no rung-2 record should be written below the watermark, got %+v", got)
	}
}

// Rung 2 belongs to the DETECTOR, not to escalation. Turning stuck_loop_escalation
// off asks not to have the model swapped; it does not ask to stop being told the
// loop is happening.
func TestStallHoldOffSurvivesEscalationBeingOff(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	a.SetStuckLoopEscalation(false)
	var got []StallRecord
	sink := collectStalls(&got)

	churnLoop(a, 5)
	a.maybeEscalate(context.Background(), sink)
	if a.stall.nudge() == "" {
		t.Fatal("rung 2 is the detector's, and must still speak with escalation switched off")
	}
	if len(got) != 1 || got[0].Rung != 2 {
		t.Errorf("and must still be recorded, got %+v", got)
	}
}

// The spin axis reaches rung 2 as well, and its record says so.
//
// The fixture has to be a PRODUCTIVE repeat — a plain successful result with no
// guard phrase in it — because anything unproductive mints a churn signature
// too and the switch in record() prefers churn. That is also why the record's
// axis is carried from the raise site rather than inferred from an empty
// detail: the read-dedup guard produces a churn signature with a NON-error
// result, so "no detail" would have mislabelled it.
func TestStallHoldOffOnTheSpinAxis(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	var got []StallRecord
	sink := collectStalls(&got)

	for i := 0; i < 5; i++ {
		a.stall.observe(
			call("b", "bash", `{"command":"ls"}`),
			result("b", "file1\nfile2", false),
		)
	}
	a.maybeEscalate(context.Background(), sink)
	if len(got) != 1 || got[0].Axis != stallAxisSpin || got[0].Rung != 2 {
		t.Fatalf("a spin loop should reach rung 2 and be recorded as spin, got %+v", got)
	}
	if d := got[0].Detail; d != "" {
		t.Errorf("spin carries no error detail, got %q", d)
	}
	if n := a.stall.nudge(); !strings.Contains(n, "same arguments and the same result") {
		t.Errorf("the spin hold-off should say the result was identical too:\n%s", n)
	}
}
