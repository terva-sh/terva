package core

import (
	"fmt"
	"strings"
	"testing"
)

// TW-030. The fixture is the Wave 3/4 archiving session
// (f35bb34caca0a141/20260726-151519-5ddc9c2f.jsonl), where a correct bounded
// loop was nudged twice.
//
// The loop archives 4,000 messages in batches of 200. Each batch is: search for
// the matching cohort at position 0, dry-run, apply. The search arguments are
// byte-identical every time ON PURPOSE — the preceding move removed that
// batch's 200 messages from the cohort, so the next position-0 query runs
// against a smaller set and returns a descending total, a new queryState, a
// disjoint id set and a new selectionId. Ten identical calls, ten different
// results, correct throughout.
//
// The spin axis keyed on arguments alone, so it read that as a repeat and told
// the model "You already have the result" — which was false, and which the
// agent then spent a turn rebutting, twice, in the middle of an audited bulk
// operation.

// archiveWave drives n batches of the real loop shape: an identical search
// whose result differs every time, then the mutator that makes it differ.
func archiveWave(tr *stallTracker, n int) {
	for i := 0; i < n; i++ {
		remaining := 4000 - i*200
		tr.observe(
			call("s", "email_search", `{"filter":{"inMailbox":"inbox"},"limit":200,"position":0}`),
			result("s", fmt.Sprintf(`{"total":%d,"queryState":"q%d","selectionId":"sel%d"}`, remaining, i, i), false),
		)
		tr.observe(
			call("m", "email_move", fmt.Sprintf(`{"selection":"sel%d","to":"archive"}`, i)),
			result("m", fmt.Sprintf(`moved 200 (%d remaining)`, remaining-200), false),
		)
	}
}

// The acceptance case: a bounded loop whose identical query returns something
// new each time is not spinning, and must not be interrupted.
func TestStallSpinIgnoresAnIdenticalCallWithChangingResults(t *testing.T) {
	var tr stallTracker
	archiveWave(&tr, 10)
	if n := tr.nudge(); n != "" {
		t.Fatalf("a bounded loop making progress must not be nudged:\n%s", n)
	}
	if _, ok := tr.escalation(); ok {
		t.Error("and it must certainly not escalate")
	}
}

// The other half of the contract, and the reason the axis still exists: when
// the result does NOT change, the same call is redundant work and still trips
// at the third recurrence. Same tool, same arguments as above — only the result
// is held still — so this isolates the result as the discriminator.
func TestStallSpinStillTripsWhenTheResultIsIdentical(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 3; i++ {
		tr.observe(
			call("s", "email_search", `{"filter":{"inMailbox":"inbox"},"limit":200,"position":0}`),
			result("s", `{"total":4000,"queryState":"q0","selectionId":"sel0"}`, false),
		)
	}
	nudge := tr.nudge()
	if nudge == "" {
		t.Fatal("three identical calls returning identical results is the spin this axis is for")
	}
	if !strings.Contains(nudge, "email_search") {
		t.Errorf("the nudge should name the repeated tool:\n%s", nudge)
	}
}

// Narrowing spin must not narrow churn, and the two axes have to stay
// independently reachable on the SAME call shape.
//
// Identical arguments, and results that are the same failure wearing a
// different exit code. normalizeError collapses the `[exit N]` tail, so the
// churn key matches; the spin fingerprint hashes the raw bytes, so the spin key
// does not. Only churn can trip here — which is the point: the fingerprint went
// onto the spin key alone.
func TestStallChurnUnaffectedByTheResultFingerprint(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 3; i++ {
		tr.observe(
			call("s", "email_search", `{"filter":{"inMailbox":"inbox"}}`),
			result("s", fmt.Sprintf("search backend unavailable [exit %d]", i+1), true),
		)
	}
	nudge := tr.nudge()
	if nudge == "" {
		t.Fatal("an error churn must still trip — the fingerprint is on the spin key only")
	}
	// The error-flavoured nudge, i.e. it really was churn that fired and not
	// spin sneaking through on some other equality.
	if !strings.Contains(nudge, "same result") {
		t.Errorf("expected the churn nudge, got:\n%s", nudge)
	}
}

// The escalation watermark rides the same key, so a genuine spin that keeps
// going still escalates. Five identical (args, result) recurrences is the
// documented watermark.
func TestStallSpinStillEscalatesOnAGenuineLoop(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 5; i++ {
		tr.observe(
			call("r", "read", `{"path":"models.json"}`),
			result("r", "models.json — unchanged since you read it earlier", false),
		)
	}
	if _, ok := tr.escalation(); !ok {
		t.Fatal("five identical calls with identical results must still reach the escalation watermark")
	}
}

// The documented limitation. The fingerprint hashes the result bytes as they
// came, so a tool that stamps its own output has no two byte-identical results
// and never trips the spin axis — even when the caller is genuinely stuck.
//
// This is a deliberate trade, recorded as a test rather than a comment so it
// cannot rot into a surprise. Normalizing timestamps out first would be
// guesswork about which parts of an arbitrary tool's output are incidental, and
// over-normalizing re-introduces the false nudge this axis was just narrowed to
// avoid. The churn axis still covers the case where such a loop is FAILING,
// which is the harmful shape; what is given up is a stuck loop whose stamped
// output keeps succeeding.
func TestStallSpinIgnoresTimestampedResults(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 5; i++ {
		tr.observe(
			call("b", "bash", `{"command":"date"}`),
			result("b", fmt.Sprintf("Sat Aug  1 10:0%d:00 UTC 2026", i), false),
		)
	}
	if tr.nudge() != "" {
		t.Fatalf("KNOWN GAP CLOSED: a stamped result now trips spin — if this is deliberate, "+
			"update the doc comment on resultFingerprint, which promises it does not:\n%s", tr.nudge())
	}
}
