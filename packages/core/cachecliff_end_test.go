package core

import "testing"

// A run stops for two unrelated reasons, and the detector used to report both
// with the same zero event. That is not a cosmetic gap: an activate_tools
// voids the baseline, so one measured 46-dispatch floor was recorded as three
// short runs that each read back as a recovery, and the spike that read them
// started from the wrong hypothesis because of it.
//
// Both arms below drive the SAME opening sequence and differ only in how the
// run stops, so anything they disagree about is the ending and nothing else.
func TestCliffEndNamesWhyTheRunStopped(t *testing.T) {
	// steps 0..3 open a run: warm the detector, then three collapses on a
	// large append-only prompt, which is the provider signature.
	opening := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728},
		{input: 60_000, cacheRead: 9_728},
	}

	t.Run("a dispatch that hits cache again is a recovery", func(t *testing.T) {
		steps := append(append([]cliffUsageStep{}, opening...),
			// Serves far more than half the previous prompt: not a collapse.
			cliffUsageStep{input: 200, cacheRead: 120_000})
		_, events, run := cliffAgent(t, steps)
		run(0, 5)

		last := lastCliff(t, events)
		if last.Ongoing {
			t.Fatalf("want a terminal event, got an ongoing one: %+v", last)
		}
		if last.End != CliffEndRecovered {
			t.Errorf("End = %q, want %q: a dispatch hit cache again, so the run is over", last.End, CliffEndRecovered)
		}
	})

	t.Run("a prefix rebuild voids the run rather than ending it", func(t *testing.T) {
		steps := append(append([]cliffUsageStep{}, opening...),
			// The collapse CONTINUES. Only the baseline went away.
			cliffUsageStep{input: 62_000, cacheRead: 9_728})
		a, events, run := cliffAgent(t, steps)
		run(0, 4)

		// What watchPrefix does on any non-append: a tool-set change, a model
		// change, a compaction. The detector cannot measure across it.
		a.bumpCliffEpoch()
		run(4, 5)

		last := lastCliff(t, events)
		if last.Ongoing {
			t.Fatalf("want a terminal event, got an ongoing one: %+v", last)
		}
		if last.End != CliffEndVoided {
			t.Errorf("End = %q, want %q: terva rebuilt the prefix, so the measurement stopped and not the collapse", last.End, CliffEndVoided)
		}
	})
}

// The ending must be the ONLY thing that separates the two, so a reader can
// trust it rather than inferring from the counts.
func TestCliffEndIsTheOnlyDifferenceBetweenTheTwoEndings(t *testing.T) {
	opening := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728},
		{input: 60_000, cacheRead: 9_728},
	}

	recovered := append(append([]cliffUsageStep{}, opening...),
		cliffUsageStep{input: 200, cacheRead: 120_000})
	_, recEvents, recRun := cliffAgent(t, recovered)
	recRun(0, 5)
	rec := lastCliff(t, recEvents)

	voided := append(append([]cliffUsageStep{}, opening...),
		cliffUsageStep{input: 62_000, cacheRead: 9_728})
	a, voidEvents, voidRun := cliffAgent(t, voided)
	voidRun(0, 4)
	a.bumpCliffEpoch()
	voidRun(4, 5)
	vd := lastCliff(t, voidEvents)

	if rec == vd {
		t.Fatal("the two endings are indistinguishable, which is the defect this test exists for")
	}
	if rec.End == vd.End {
		t.Errorf("both endings carry End = %q", rec.End)
	}
	// Both terminal events carry zero counts by contract: the host holds the
	// last ongoing event and supplies the run's totals when it writes the row.
	if rec.Dispatches != 0 || rec.RereadTokens != 0 {
		t.Errorf("recovered terminal event carries counts %+v, want zeroes", rec)
	}
	if vd.Dispatches != 0 || vd.RereadTokens != 0 {
		t.Errorf("voided terminal event carries counts %+v, want zeroes", vd)
	}
}

func lastCliff(t *testing.T, events *[]CacheCliff) CacheCliff {
	t.Helper()
	if len(*events) == 0 {
		t.Fatal("no cliff events fired at all, so the run never opened and this test proves nothing")
	}
	return (*events)[len(*events)-1]
}
