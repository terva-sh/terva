package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// pressureRun drives n turns at a fixed context usage and reports how many of
// them carried the note. Driven through Prompt rather than by calling the peek
// directly: the cadence is only correct if composeTail consults it AND oneTurn
// commits afterwards, and testing the helper alone would prove neither.
func pressureRun(t *testing.T, a *Agent, client *reqCaptureClient, used, turns int) int {
	t.Helper()
	before := len(client.ephemeral)
	for i := 0; i < turns; i++ {
		a.SeedLastTurnUsage(provider.Usage{InputTokens: used})
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}
	carried := 0
	for _, e := range client.ephemeral[before:] {
		if strings.Contains(e, "[context pressure]") {
			carried++
		}
	}
	return carried
}

// The regression, in the numbers it was measured in. A real session put the
// note on 74 of 407 requests — 18% — because past the threshold it rode every
// single one. Sitting at one level must now cost a handful of reminders, not a
// note per request.
func TestSittingAtOneLevelDoesNotWarnEveryRequest(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	// 195k of a 272k window = 71%: past the warn line, inside the first band.
	carried := pressureRun(t, a, client, 195_000, 30)

	if carried == 30 {
		t.Fatal("the note rode every request — this is the level-trigger behaviour the change removes")
	}
	// One on entry, then the repeat interval — band 1's, which is the slowest on
	// the ladder because 71% is the case with the most room to react. Anything
	// much denser is the old behaviour wearing a hat.
	if want := 1 + 30/contextPressureRepeatEvery(1); carried > want {
		t.Errorf("note carried %d times over 30 requests, want at most %d", carried, want)
	}
	if carried == 0 {
		t.Error("the note never rode at all — the model is now never warned")
	}
}

// Entering the band must say so immediately. A warning that waits for an
// interval is a warning that arrives after the expensive read it existed to
// prevent.
func TestCrossingIntoTheBandWarnsAtOnce(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	if carried := pressureRun(t, a, client, 195_000, 1); carried != 1 {
		t.Errorf("the first request past the threshold carried the note %d times, want 1", carried)
	}
}

// Escalation is news even mid-interval: 71% and 86% are different situations,
// and the second must not be silenced by having recently mentioned the first.
func TestClimbingIntoANewBandWarnsAgainImmediately(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	pressureRun(t, a, client, 195_000, 1) // enter band 1
	if carried := pressureRun(t, a, client, 220_000, 1); carried != 1 {
		t.Errorf("crossing into the 80%% band did not warn (carried %d)", carried)
	}
	if carried := pressureRun(t, a, client, 235_000, 1); carried != 1 {
		t.Errorf("crossing into the 85%% band did not warn (carried %d)", carried)
	}
}

// The flapping this replaces: the gauge is the last request's input count and
// does not move monotonically, so a transcript hovering at the line crossed it
// repeatedly and the note appeared, vanished and returned. Hysteresis means a
// dip that never really relieved anything does not re-announce the same band.
func TestAGaugeHoveringOnTheLineDoesNotReAnnounce(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	pressureRun(t, a, client, 191_000, 1) // 70.2% — enters the band, warns
	// Jitter around the boundary: just under, then just over, repeatedly.
	carried := 0
	for i := 0; i < 4; i++ {
		carried += pressureRun(t, a, client, 189_000, 1) // 69.5%
		carried += pressureRun(t, a, client, 191_000, 1) // 70.2%
	}
	if carried > 0 {
		t.Errorf("jittering across the threshold re-announced the same band %d times", carried)
	}
}

// ...but a real recovery must reset, or a session that compacts and then climbs
// all the way back would never be warned a second time.
func TestACompactionThatRelievesPressureRearmsTheWarning(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	pressureRun(t, a, client, 195_000, 1) // warned
	pressureRun(t, a, client, 40_000, 1)  // a compaction lands: 15%
	if carried := pressureRun(t, a, client, 195_000, 1); carried != 1 {
		t.Errorf("after real relief the climb back past the line did not warn (carried %d)", carried)
	}
}

// Below the line, nothing — the note must not leak into ordinary turns.
func TestBelowTheThresholdNothingIsSaid(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})

	if carried := pressureRun(t, a, client, 50_000, 5); carried != 0 {
		t.Errorf("a comfortable session was warned %d times", carried)
	}
}

// The note is a last-in-turn ephemeral block, the same shape as the
// inactive-groups note that ended 80 of 80 transcripts with the model answering
// the NOTE instead of the user. The review that produced the band ladder
// recorded this note's own version of that failure — a model "narrating its
// context budget back at the user" — and prohibition-first is what measured
// 20-of-20 answers back on the sibling note. A prohibition buried after the
// detail it governs loses completely on a weak model, so ORDER is the assertion.
func TestTheNoteLeadsWithTheDoNotReplyGuard(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})
	pressureRun(t, a, client, 195_000, 1)

	eph := client.ephemeral[0]
	guard := strings.Index(eph, "Do not reply to this note")
	if guard < 0 {
		t.Fatalf("the note carries no do-not-reply guard: %q", eph)
	}
	gauge := strings.Index(eph, "% full")
	if gauge < 0 {
		t.Fatalf("the note carries no gauge: %q", eph)
	}
	if guard > gauge {
		t.Error("the gauge precedes the prohibition; prohibition-first is the measured ordering")
	}
	if !strings.Contains(eph, "as if the note were not here") {
		t.Errorf("the guard does not tell the model what to do instead: %q", eph)
	}
}

// One text for the whole ladder was the tonal defect behind the rewrite:
// entering at 70% read exactly as urgently as arriving at 86%. Asserted on the
// advice directly rather than on rendered notes, which differ by percentage
// anyway and would pass this even if every band said the same thing.
func TestTheAdviceGraduatesWithTheBand(t *testing.T) {
	seen := map[string]bool{}
	for band := 1; band <= len(contextPressureBands); band++ {
		a := contextPressureAdvice(band)
		if a == "" {
			t.Fatalf("band %d has no advice", band)
		}
		if seen[a] {
			t.Errorf("band %d repeats an earlier band's advice: %q", band, a)
		}
		seen[a] = true
	}

	// ...and the band the note was rendered for is the band whose advice it
	// carries. Without this the graduation above could be dead code.
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})
	pressureRun(t, a, client, 235_000, 1) // 86% — band 3
	eph := client.ephemeral[0]
	if !strings.Contains(eph, contextPressureAdvice(3)) {
		t.Errorf("an 86%% note did not carry band 3's advice: %q", eph)
	}
	if strings.Contains(eph, contextPressureAdvice(1)) {
		t.Errorf("an 86%% note carried band 1's advice: %q", eph)
	}
}

func TestBandBoundaries(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		want int
	}{
		{0.00, 0}, {0.69, 0}, {0.70, 1}, {0.77, 1},
		{0.78, 2}, {0.84, 2}, {0.85, 3}, {0.91, 3},
		{0.92, 4}, {1.00, 4},
	} {
		if got := contextPressureBand(tc.f); got != tc.want {
			t.Errorf("contextPressureBand(%.2f) = %d, want %d", tc.f, got, tc.want)
		}
	}
}

// Two rungs of the ladder are not free parameters. The first is what
// commitContextPressure clears against, so a ladder that starts anywhere else
// would arm and disarm at different fractions and re-announce forever; and the
// band the note calls "terva compacts when this turn ends" has to be the band
// where terva actually does that. Both couplings live in prose in two files,
// which is exactly the kind that rots.
func TestLadderMatchesPolicy(t *testing.T) {
	if got := contextPressureBands[0].at; got != ContextWarnFraction {
		t.Errorf("first band is %.2f, ContextWarnFraction is %.2f — the clear margin is measured off the second", got, ContextWarnFraction)
	}
	// Band 3 is the one contextPressurePolicy switches its closing sentence on.
	if got := contextPressureBands[2].at; got != AutoCompactThreshold {
		t.Errorf("band 3 is %.2f, AutoCompactThreshold is %.2f — the note would promise a valve at the wrong fraction", got, AutoCompactThreshold)
	}
}

// The reminder interval must tighten as the window fills. A flat interval was
// the old behaviour and it is wrong at one end or the other: the same cadence
// cannot serve 71%, where there is a phase of work left, and 93%, where there
// are a few requests.
func TestReminderIntervalTightensWithPressure(t *testing.T) {
	for i := 1; i < len(contextPressureBands); i++ {
		prev, cur := contextPressureRepeatEvery(i), contextPressureRepeatEvery(i+1)
		if cur >= prev {
			t.Errorf("band %d repeats every %d requests, band %d every %d — the ladder must get more urgent, not less", i, prev, i+1, cur)
		}
	}
	// Off the ladder there is no interval to give, and returning a real one
	// would let a below-threshold request satisfy the cadence check.
	if got := contextPressureRepeatEvery(0); got != 0 {
		t.Errorf("contextPressureRepeatEvery(0) = %d, want 0", got)
	}
	if got := contextPressureRepeatEvery(len(contextPressureBands) + 1); got != 0 {
		t.Errorf("contextPressureRepeatEvery past the ladder = %d, want 0", got)
	}
}
