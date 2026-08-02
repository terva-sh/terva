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
	// One on entry, then the repeat interval. Anything much denser is the old
	// behaviour wearing a hat.
	if want := 1 + 30/contextPressureRepeatEvery; carried > want {
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

func TestBandBoundaries(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		want int
	}{
		{0.00, 0}, {0.69, 0}, {0.70, 1}, {0.79, 1},
		{0.80, 2}, {0.85, 3}, {0.90, 4}, {0.95, 5}, {1.00, 5},
	} {
		if got := contextPressureBand(tc.f); got != tc.want {
			t.Errorf("contextPressureBand(%.2f) = %d, want %d", tc.f, got, tc.want)
		}
	}
}
