package provider

import (
	"net/http"
	"testing"
	"time"
)

// epoch2030 is a fixed, plausible reset timestamp used so reset parsing
// is deterministic (no clock dependence).
const epoch2030 = int64(1893456000) // 2030-01-01T00:00:00Z

func TestParseCodexUsageHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		wantOK  bool
		check   func(t *testing.T, s UsageSnapshot)
	}{
		{
			name: "both windows + credits",
			headers: map[string]string{
				"x-codex-primary-used-percent":     "42.5",
				"x-codex-primary-window-minutes":   "300",
				"x-codex-primary-reset-at":         "1893456000",
				"x-codex-secondary-used-percent":   "88",
				"x-codex-secondary-window-minutes": "10080",
				"x-codex-secondary-reset-at":       "1893456000",
				"x-codex-credits-has-credits":      "true",
				"x-codex-credits-balance":          "12.5",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 2 {
					t.Fatalf("windows = %d, want 2", len(s.Windows))
				}
				p := s.Windows[0]
				if p.Label != "5h" || p.UsedPercent != 42.5 || p.WindowMinutes != 300 {
					t.Errorf("primary = %+v", p)
				}
				if !p.ResetsAt.Equal(time.Unix(epoch2030, 0).UTC()) {
					t.Errorf("primary reset = %v, want %v", p.ResetsAt, time.Unix(epoch2030, 0).UTC())
				}
				sec := s.Windows[1]
				if sec.Label != "weekly" || sec.UsedPercent != 88 || sec.WindowMinutes != 10080 {
					t.Errorf("secondary = %+v", sec)
				}
				if s.Credits == nil || !s.Credits.HasCredits || s.Credits.Balance != 12.5 {
					t.Errorf("credits = %+v", s.Credits)
				}
			},
		},
		{
			name:    "primary only",
			headers: map[string]string{"x-codex-primary-used-percent": "10", "x-codex-primary-window-minutes": "300"},
			wantOK:  true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 1 || s.Windows[0].Label != "5h" {
					t.Fatalf("windows = %+v", s.Windows)
				}
				if s.Credits != nil {
					t.Errorf("credits = %+v, want nil", s.Credits)
				}
			},
		},
		{
			name:    "no usage headers",
			headers: map[string]string{"content-type": "text/event-stream", "retry-after": "5"},
			wantOK:  false,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 0 || s.Credits != nil {
					t.Errorf("expected empty snapshot, got %+v", s)
				}
			},
		},
		{
			name:    "credits only, no windows",
			headers: map[string]string{"x-codex-credits-unlimited": "true"},
			wantOK:  true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 0 {
					t.Errorf("windows = %+v, want none", s.Windows)
				}
				if s.Credits == nil || !s.Credits.Unlimited {
					t.Errorf("credits = %+v", s.Credits)
				}
			},
		},
		{
			name: "malformed percent stays unknown, window still emitted",
			headers: map[string]string{
				"x-codex-primary-used-percent":   "n/a",
				"x-codex-primary-window-minutes": "300",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 1 {
					t.Fatalf("windows = %d, want 1", len(s.Windows))
				}
				if s.Windows[0].UsedPercent != -1 {
					t.Errorf("UsedPercent = %v, want -1 (unknown)", s.Windows[0].UsedPercent)
				}
			},
		},
		{
			name: "reset-at as RFC3339",
			headers: map[string]string{
				"x-codex-primary-used-percent": "5",
				"x-codex-primary-reset-at":     "2030-01-01T00:00:00Z",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if !s.Windows[0].ResetsAt.Equal(time.Unix(epoch2030, 0).UTC()) {
					t.Errorf("reset = %v", s.Windows[0].ResetsAt)
				}
			},
		},
		{
			name: "reset-at small int treated as unknown, not wrong epoch",
			headers: map[string]string{
				"x-codex-primary-used-percent": "5",
				"x-codex-primary-reset-at":     "3600",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if !s.Windows[0].ResetsAt.IsZero() {
					t.Errorf("reset = %v, want zero", s.Windows[0].ResetsAt)
				}
			},
		},
		{
			// Captured verbatim from a live turn on 2026-07-13, the day after
			// OpenAI lifted the Codex 5h limit: the weekly limit is reported in
			// the PRIMARY slot and the secondary is an all-zero placeholder
			// (percent 0, minutes 0, an empty reset-at). Emitting a window for
			// that placeholder put a second, empty "weekly" bar — it has no
			// duration to name itself by, so it fell back to its slot's label —
			// under the real one.
			name: "lifted 5h limit: the zeroed secondary is not a window",
			headers: map[string]string{
				"x-codex-primary-used-percent":                 "19",
				"x-codex-primary-window-minutes":               "10080",
				"x-codex-primary-reset-at":                     "1784488125",
				"x-codex-primary-reset-after-seconds":          "533939",
				"x-codex-secondary-used-percent":               "0",
				"x-codex-secondary-window-minutes":             "0",
				"x-codex-secondary-reset-at":                   "",
				"x-codex-secondary-reset-after-seconds":        "0",
				"x-codex-plan-type":                            "prolite",
				"x-codex-active-limit":                         "premium",
				"x-codex-primary-over-secondary-limit-percent": "0",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 1 {
					t.Fatalf("windows = %+v, want just the weekly one", s.Windows)
				}
				w := s.Windows[0]
				if w.Label != "weekly" || w.UsedPercent != 19 || w.WindowMinutes != 10080 {
					t.Errorf("window = %+v, want weekly at 19%%", w)
				}
				if !w.ResetsAt.Equal(time.Unix(1784488125, 0).UTC()) {
					t.Errorf("reset = %v", w.ResetsAt)
				}
			},
		},
		{
			// The backend also reports per-model limit families under their own
			// prefix (x-codex-bengalfox-* is GPT-5.3-Codex-Spark's bucket, and
			// the Codex CLI discovers them by scanning for *-primary-used-percent).
			// We read only the account-wide `codex` family; a sibling family's
			// headers must not leak into it — least of all bengalfox's idle 0%,
			// which would resurrect the empty bar under a different name.
			name: "a per-model limit family is not mistaken for the account's",
			headers: map[string]string{
				"x-codex-primary-used-percent":             "19",
				"x-codex-primary-window-minutes":           "10080",
				"x-codex-bengalfox-limit-name":             "GPT-5.3-Codex-Spark",
				"x-codex-bengalfox-primary-used-percent":   "0",
				"x-codex-bengalfox-primary-window-minutes": "10080",
				"x-codex-bengalfox-primary-reset-at":       "1784558987",
				"x-codex-bengalfox-secondary-used-percent": "0",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 1 || s.Windows[0].UsedPercent != 19 {
					t.Fatalf("windows = %+v, want only the account-wide weekly", s.Windows)
				}
			},
		},
		{
			// …but a limit that is genuinely in force and merely unused still has
			// a duration, so it keeps its bar. 0% is news; nothing at all is not.
			name: "a real window at 0% survives",
			headers: map[string]string{
				"x-codex-secondary-used-percent":   "0",
				"x-codex-secondary-window-minutes": "10080",
			},
			wantOK: true,
			check: func(t *testing.T, s UsageSnapshot) {
				if len(s.Windows) != 1 || s.Windows[0].UsedPercent != 0 || s.Windows[0].Label != "weekly" {
					t.Fatalf("windows = %+v", s.Windows)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			got, ok := parseCodexUsageHeaders(h)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestWindowLabel(t *testing.T) {
	cases := []struct {
		minutes int
		want    string
	}{
		{0, "fallback"},
		{-5, "fallback"},
		{300, "5h"},
		{60, "1h"},
		{1440, "daily"},
		{10080, "weekly"},
		{20160, "2w"},
		{2880, "2d"},
		{90, "90m"},
		// The backend rounds: these are the durations it really sends for the
		// windows it calls 5-hour and weekly. Exact-multiple matching rendered
		// them "299m" and "10079m".
		{299, "5h"},
		{10079, "weekly"},
		{1435, "daily"},
		// …but tolerance is ±5%, so a duration that is genuinely its own thing
		// is still named for what it is rather than rounded into a neighbour.
		{330, "330m"},
		{45, "45m"},
	}
	for _, tc := range cases {
		if got := windowLabel(tc.minutes, "fallback"); got != tc.want {
			t.Errorf("windowLabel(%d) = %q, want %q", tc.minutes, got, tc.want)
		}
	}
}

// TestClientUsageThroughWrappers mirrors TestClientMirrorsToolImagesThroughWrappers:
// usage MUST survive the wrappers the build layer actually ships —
// openai-responses rides behind a renamedClient. A direct assertion on the
// outer client would silently report "no usage", so ClientUsage unwraps
// recursively.
func TestClientUsageThroughWrappers(t *testing.T) {
	// A fresh codex client has seen no response yet → nothing to report.
	raw := NewOpenAICodex("k", "", "")
	if _, ok := ClientUsage(raw); ok {
		t.Fatal("fresh codex client should report no usage")
	}

	// Populate it as a live response would.
	cx := raw.(*codexClient)
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "42")
	h.Set("x-codex-primary-window-minutes", "300")
	cx.recordUsageHeaders(h)

	cases := []struct {
		name string
		c    Client
	}{
		{"raw", raw},
		{"renamed", &renamedClient{inner: raw, name: "openai-responses"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := ClientUsage(tc.c)
			if !ok {
				t.Fatalf("ClientUsage(%s) ok=false; want usage through the wrapper", tc.name)
			}
			if s.Provider != "openai-codex" {
				t.Errorf("Provider = %q, want openai-codex", s.Provider)
			}
			if len(s.Windows) != 1 || s.Windows[0].Label != "5h" {
				t.Errorf("windows = %+v", s.Windows)
			}
		})
	}

	// A provider with no reporter (plain OpenAI chat) returns ok=false,
	// not a zero snapshot mistaken for real data.
	if _, ok := ClientUsage(NewOpenAI("k", "")); ok {
		t.Error("openai chat client should report no usage")
	}
}
