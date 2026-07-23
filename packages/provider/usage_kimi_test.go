package provider

import (
	"testing"
	"time"
)

// liveKimiUsagesBody is a real GET /coding/v1/usages response (2026-07-22),
// trimmed to the fields the parser reads. Numbers are strings; resetTime is
// RFC3339 with fractional seconds; the aggregate carries `used`, the sub-window
// carries only remaining.
const liveKimiUsagesBody = `{
  "user": {"userId": "x", "membership": {"level": "LEVEL_INTERMEDIATE"}},
  "usage": {"limit": "100", "used": "2", "remaining": "98", "resetTime": "2026-07-29T18:07:16.062656Z"},
  "limits": [
    {"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
     "detail": {"limit": "100", "remaining": "100", "resetTime": "2026-07-23T04:07:16.062656Z"}}
  ],
  "parallel": {"limit": "20"}
}`

func TestParseKimiUsage(t *testing.T) {
	snap, ok := parseKimiUsage([]byte(liveKimiUsagesBody))
	if !ok {
		t.Fatal("parseKimiUsage: ok=false, want true")
	}
	if snap.Provider != "kimi" {
		t.Errorf("Provider = %q, want kimi", snap.Provider)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2", len(snap.Windows))
	}

	weekly := snap.Windows[0]
	if weekly.Label != "weekly" {
		t.Errorf("weekly.Label = %q, want weekly", weekly.Label)
	}
	if weekly.UsedPercent != 2 {
		t.Errorf("weekly.UsedPercent = %v, want 2", weekly.UsedPercent)
	}
	if weekly.Kind != WindowPlan {
		t.Errorf("weekly.Kind = %v, want WindowPlan", weekly.Kind)
	}
	wantWeeklyReset, _ := time.Parse(time.RFC3339, "2026-07-29T18:07:16.062656Z")
	if !weekly.ResetsAt.Equal(wantWeeklyReset) {
		t.Errorf("weekly.ResetsAt = %v, want %v", weekly.ResetsAt, wantWeeklyReset)
	}

	fiveH := snap.Windows[1]
	if fiveH.Label != "5h" {
		t.Errorf("5h.Label = %q, want 5h", fiveH.Label)
	}
	if fiveH.WindowMinutes != 300 {
		t.Errorf("5h.WindowMinutes = %d, want 300", fiveH.WindowMinutes)
	}
	// remaining==limit and no `used` field → 0% consumed.
	if fiveH.UsedPercent != 0 {
		t.Errorf("5h.UsedPercent = %v, want 0", fiveH.UsedPercent)
	}
	if fiveH.Kind != WindowPlan {
		t.Errorf("5h.Kind = %v, want WindowPlan", fiveH.Kind)
	}
}

func TestParseKimiUsageEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"usage":{},"limits":[]}`, `not json`} {
		if _, ok := parseKimiUsage([]byte(body)); ok {
			t.Errorf("parseKimiUsage(%q): ok=true, want false", body)
		}
	}
}

func TestKimiWindowMinutes(t *testing.T) {
	cases := []struct {
		duration int
		unit     string
		want     int
	}{
		{300, "TIME_UNIT_MINUTE", 300},
		{5, "HOUR", 300},
		{7, "TIME_UNIT_DAY", 10080},
		{1, "WEEK", 10080},
		{1, "TIME_UNIT_MONTH", 43200},
		{10, "TIME_UNIT_UNSPECIFIED", 0},
	}
	for _, c := range cases {
		if got := kimiWindowMinutes(c.duration, c.unit); got != c.want {
			t.Errorf("kimiWindowMinutes(%d,%q) = %d, want %d", c.duration, c.unit, got, c.want)
		}
	}
}

// TestKimiClientWrappedForUsage asserts the live constructor returns a client
// that pulls usage from an endpoint (a UsageRefresher), not a passive header
// reporter — i.e. it took the poll seam, and the wrap did not hide that.
func TestKimiClientWrappedForUsage(t *testing.T) {
	c := NewKimiCodingSourceWithHeaders(StaticCredential("x"), "", nil)
	if !ClientNeedsUsageFetch(c) {
		t.Error("kimi client should be a UsageRefresher (poll-based usage)")
	}
	// The anthropic capability must survive the wrapper (probed via Unwrap).
	if !ClientCaps(c).ContinuesAssistantPrefill {
		t.Error("kimi (anthropic) ContinuesAssistantPrefill lost through the usage wrapper")
	}
}
