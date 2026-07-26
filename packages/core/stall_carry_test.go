package core

import (
	"fmt"
	"strings"
	"testing"
)

// The fixture is the Wave 3 archiving session
// (f35bb34caca0a141/20260726-060056-87df6b73.jsonl), where one email_move
// signature failed 19 times across a turn boundary. The tracker nudged in each
// turn — at that turn's THIRD recurrence both times, because reset() had wiped
// every trace of the first — so a loop already thirteen failures deep was
// treated as brand new.
const carryErr = "pass exactly one of ids, selection, or receipt — each names the whole set on its own"

// churnTurn drives n identical failing calls through the tracker.
func churnTurn(tr *stallTracker, n int) {
	for i := 0; i < n; i++ {
		// Vary the argument the way the real loop did (placeholder, x, dummy…)
		// so this rides the churn axis rather than spin, exactly as it did live.
		tr.observe(call("m", "email_move", fmt.Sprintf(`{"ids":["%d"]}`, i)), result("m", carryErr, true))
	}
}

// A loop that survives a turn boundary resumes where it left off. It still has
// to re-establish a local pattern before anything fires — that part is
// deliberate — but once it does, the nudge reports the run's true length and the
// escalation watermark it already crossed does not have to be crossed again.
func TestStallCarriesARecurringSignatureAcrossTheTurnBoundary(t *testing.T) {
	var tr stallTracker
	churnTurn(&tr, 13)
	if _, ok := tr.escalation(); !ok {
		t.Fatal("precondition: 13 recurrences in one turn should have raised an escalation")
	}
	tr.markEscalated()
	tr.reset()

	// Two recurrences is not yet a local pattern: the ladder is remembered, not
	// short-circuited.
	churnTurn(&tr, 2)
	if tr.nudge() != "" {
		t.Fatalf("two calls after the boundary must not trip; a new turn needs its own pattern:\n%s", tr.nudge())
	}
	if _, ok := tr.escalation(); ok {
		t.Error("nothing should escalate before the signature trips in this turn")
	}

	// The third establishes it, and now the carried history counts.
	churnTurn(&tr, 1)
	nudge := tr.nudge()
	if nudge == "" {
		t.Fatal("the third recurrence after the boundary must trip")
	}
	if !strings.Contains(nudge, "16") {
		t.Errorf("the nudge should report the run's true length (13 + 3 = 16), not this turn's 3:\n%s", nudge)
	}
	if _, ok := tr.escalation(); !ok {
		t.Error("a signature that already crossed the escalation watermark must not have to cross it again from zero")
	}
}

// The carry is scoped to loops that are still live. A signature the model
// stopped repeating is forgotten at the next boundary, so the map cannot
// accumulate across a long session.
func TestStallCarryDropsSignaturesThatStopRecurring(t *testing.T) {
	var tr stallTracker
	churnTurn(&tr, 4)
	tr.reset()
	if len(tr.carried) == 0 {
		t.Fatal("precondition: a signature that recurred should have been carried")
	}
	// Only the loop itself crosses. A churn loop varies its arguments, so each
	// call also mints a fresh spin signature seen exactly once; carrying those
	// would haul a turn's worth of dead keys over every boundary.
	if len(tr.carried) != 1 {
		t.Errorf("only the recurring signature should cross, got %d: %v", len(tr.carried), tr.carried)
	}
	for sig, n := range tr.carried {
		if n < 2 {
			t.Errorf("a signature seen once is not a loop, but %q crossed with n=%d", sig, n)
		}
	}

	// A turn in which it does not recur at all.
	tr.observe(call("o", "read", `{"path":"a"}`), result("o", "fine", false))
	tr.reset()
	for sig := range tr.carried {
		if strings.Contains(sig, "email_move") {
			t.Errorf("a signature that stopped recurring must not survive the boundary: %q", sig)
		}
	}

	// And having been forgotten, it starts from scratch: three recurrences trip,
	// but the count is this turn's alone and nothing escalates early.
	churnTurn(&tr, 3)
	if tr.nudge() == "" {
		t.Fatal("it should still trip on its own merits")
	}
	if !strings.Contains(tr.nudge(), "3") {
		t.Errorf("a forgotten signature should report a fresh count:\n%s", tr.nudge())
	}
	if _, ok := tr.escalation(); ok {
		t.Error("a forgotten signature must not carry the old escalation watermark")
	}
}

// The case reset()'s comment is about: the user asking again. A fresh ask
// produces a call that has not been recurring, so there is nothing to carry and
// the turn starts clean — the carry must not make an ordinary repeat across two
// turns look like a loop.
func TestStallCarryIgnoresAnOrdinaryRepeatAcrossTurns(t *testing.T) {
	var tr stallTracker
	tr.observe(call("r", "read", `{"path":"a"}`), result("r", "contents", false))
	tr.reset()
	tr.observe(call("r", "read", `{"path":"a"}`), result("r", "contents", false))
	tr.reset()
	tr.observe(call("r", "read", `{"path":"a"}`), result("r", "contents", false))

	if tr.nudge() != "" {
		t.Errorf("one call per turn across three turns is the user re-asking, not a stall:\n%s", tr.nudge())
	}
	if _, ok := tr.escalation(); ok {
		t.Error("re-asking must not escalate")
	}
}

// "Keep trying" is the user overruling the ladder, so the ladder starts over —
// including its cross-turn memory. Leaving it in place would re-cross the
// watermark on the first recurrence and re-offer immediately, which is the
// re-asking that declines exists to prevent.
func TestStallForgiveClearsTheCarriedHistory(t *testing.T) {
	var tr stallTracker
	churnTurn(&tr, 13)
	if _, ok := tr.escalation(); !ok {
		t.Fatal("precondition: should have escalated")
	}
	tr.forgive()
	if len(tr.carried) != 0 || len(tr.seen) != 0 {
		t.Fatalf("forgive must wipe cross-turn history, got carried=%v seen=%v", tr.carried, tr.seen)
	}
	tr.reset()

	churnTurn(&tr, 3)
	if _, ok := tr.escalation(); ok {
		t.Error("after a decline, a re-tripped signature must not escalate again immediately")
	}
}

// A loop that never crosses a boundary must behave exactly as before: the carry
// is invisible to it.
func TestStallCarryDoesNotChangeASingleTurnLoop(t *testing.T) {
	var tr stallTracker
	churnTurn(&tr, 3)
	first := tr.nudge()
	if first == "" || !strings.Contains(first, "3") {
		t.Fatalf("a fresh single-turn loop should trip reporting 3:\n%s", first)
	}
	if _, ok := tr.escalation(); ok {
		t.Error("three recurrences is the nudge rung, not the escalation rung")
	}
	churnTurn(&tr, 2)
	if _, ok := tr.escalation(); !ok {
		t.Error("five recurrences in one turn should escalate, as before")
	}
}
