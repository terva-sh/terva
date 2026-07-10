package dialogs

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestUsageDialogNoData(t *testing.T) {
	d := NewUsageDialog()
	d.Open("openai-codex", provider.UsageSnapshot{}, false, false)
	out := joined(d.Render(tui.Theme{}, 80))
	if !strings.Contains(out, "openai-codex doesn't report usage limits.") {
		t.Errorf("no-data render missing the explanation:\n%s", out)
	}
}

func TestUsageDialogRendersWindowsAndCredits(t *testing.T) {
	d := NewUsageDialog()
	snap := provider.UsageSnapshot{
		Provider: "openai-codex",
		Windows: []provider.UsageWindow{
			{Label: "5h", UsedPercent: 42}, // zero ResetsAt → no countdown, deterministic
			{Label: "weekly", UsedPercent: 88},
		},
		Credits: &provider.Credits{HasCredits: true, Balance: 12.5},
	}
	d.Open("openai-codex", snap, true, false)
	out := joined(d.Render(tui.Theme{}, 80))
	for _, want := range []string{"5h", "weekly", "42%", "88%", "credits: 12.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestUsageDialogUnknownPercent(t *testing.T) {
	d := NewUsageDialog()
	snap := provider.UsageSnapshot{
		Provider: "openai-codex",
		Windows:  []provider.UsageWindow{{Label: "5h", UsedPercent: -1}},
	}
	d.Open("openai-codex", snap, true, false)
	out := joined(d.Render(tui.Theme{}, 80))
	if !strings.Contains(out, "?") {
		t.Errorf("unknown-percent window should render '?':\n%s", out)
	}
}

// A poll-based provider opens with "fetching…" (loading, no data yet); Update
// then swaps in the fetched snapshot and clears the loading state.
func TestUsageDialogLoadingThenUpdate(t *testing.T) {
	d := NewUsageDialog()
	d.Open("openrouter", provider.UsageSnapshot{}, false, true)
	if out := joined(d.Render(tui.Theme{}, 80)); !strings.Contains(out, "fetching usage") {
		t.Errorf("loading render should show a fetching line:\n%s", out)
	}
	d.Update(provider.UsageSnapshot{Provider: "openrouter", Credits: &provider.Credits{HasCredits: true, Balance: 7.5}}, true)
	out := joined(d.Render(tui.Theme{}, 80))
	if strings.Contains(out, "fetching usage") {
		t.Errorf("after Update the fetching line should be gone:\n%s", out)
	}
	if !strings.Contains(out, "credits: 7.50 remaining") {
		t.Errorf("after Update the credits should render:\n%s", out)
	}
}

// formatCredits shows remaining and/or used depending on what the provider
// reports: DeepSeek gives a balance; an uncapped OpenRouter key gives spend.
func TestFormatCreditsVariants(t *testing.T) {
	th := tui.Theme{}
	cases := []struct {
		name string
		c    provider.Credits
		want string
	}{
		{"balance + used", provider.Credits{HasCredits: true, Balance: 5, Used: 20}, "credits: 5.00 remaining  (20.00 used)"},
		{"used only", provider.Credits{Used: 12.34}, "credits: 12.34 used"},
		{"unlimited", provider.Credits{Unlimited: true}, "credits: unlimited"},
		{"none", provider.Credits{}, "credits: none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatCredits(th, &tc.c); !strings.Contains(got, tc.want) {
				t.Errorf("formatCredits = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The dialog shows rate-limit windows under a "rate limits" group label,
// alongside credits (the merged view).
func TestUsageDialogGroupsRateLimitWindows(t *testing.T) {
	d := NewUsageDialog()
	snap := provider.UsageSnapshot{
		Provider: "openrouter",
		Windows: []provider.UsageWindow{
			{Label: "requests", UsedPercent: 30, Kind: provider.WindowRateLimit},
			{Label: "tokens", UsedPercent: 20, Kind: provider.WindowRateLimit},
		},
		Credits: &provider.Credits{HasCredits: true, Balance: 9},
	}
	d.Open("openrouter", snap, true, false)
	out := joined(d.Render(tui.Theme{}, 80))
	for _, want := range []string{"rate limits", "requests", "tokens", "credits: 9.00 remaining"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestUsageDialogEscCloses(t *testing.T) {
	d := NewUsageDialog()
	d.Open("openai-codex", provider.UsageSnapshot{}, false, false)
	if !d.Active() {
		t.Fatal("dialog should be active after Open")
	}
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !closed {
		t.Error("esc should report closed=true")
	}
	if d.Active() {
		t.Error("dialog should be inactive after esc")
	}
}

func TestFormatReset(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		reset time.Time
		want  string
	}{
		{"zero is unknown", time.Time{}, ""},
		{"past is now", now.Add(-time.Minute), "resets now"},
		{"hours and minutes", now.Add(2*time.Hour + 14*time.Minute), "resets in 2h14m"},
		{"sub-minute", now.Add(30 * time.Second), "resets in <1m"},
		{"days and hours", now.Add(50 * time.Hour), "resets in 2d2h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatReset(tc.reset, now); got != tc.want {
				t.Errorf("formatReset = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{45 * time.Minute, "45m"},
		{20 * time.Second, "<1m"},
		{3 * 24 * time.Hour, "3d"},
		{(2*24 + 5) * time.Hour, "2d5h"},
	}
	for _, tc := range cases {
		if got := humanizeDuration(tc.d); got != tc.want {
			t.Errorf("humanizeDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestUsageBar(t *testing.T) {
	cases := []struct {
		pct        float64
		cells      int
		wantFilled int
	}{
		{0, 10, 0},
		{100, 10, 10},
		{50, 10, 5},
		{-5, 10, 0},   // clamped
		{150, 10, 10}, // clamped
	}
	for _, tc := range cases {
		bar := usageBar(tc.pct, tc.cells)
		if got := strings.Count(bar, "█"); got != tc.wantFilled {
			t.Errorf("usageBar(%v,%d) filled = %d, want %d (%q)", tc.pct, tc.cells, got, tc.wantFilled, bar)
		}
	}
}
