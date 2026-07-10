package workspace

import (
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

func sampleWindows() []provider.UsageWindow {
	reset := time.Date(2026, 7, 9, 17, 0, 0, 0, time.UTC)
	return []provider.UsageWindow{
		{Label: "5h", UsedPercent: 40, Kind: provider.WindowPlan, ResetsAt: reset},
		{Label: "weekly", UsedPercent: 12, Kind: provider.WindowCredit},
		{Label: "rpm", UsedPercent: 90, Kind: provider.WindowRateLimit},
	}
}

// Two shapes, on purpose, and they must not drift into each other.
//
// ContextBreakdown.UsageWindows is the status picture: rate-limit windows are
// ephemeral and would churn an always-visible meter, so the daemon drops them.
// The web client's context pane renders exactly this list.
//
// UsageInfo.Windows is the provider's whole picture, rate-limit windows
// included, because the /usage modal shows them. Filtering is the client's
// business.
func TestUsageWindowsDropsRateLimitButAllUsageWindowsKeepsIt(t *testing.T) {
	status := usageWindows(sampleWindows())
	if len(status) != 2 {
		t.Fatalf("status windows = %d, want 2 (plan + credit)", len(status))
	}
	for _, w := range status {
		if w.Kind == "rate_limit" {
			t.Fatal("a rate-limit window reached ContextBreakdown.UsageWindows")
		}
	}

	all := allUsageWindows(sampleWindows())
	if len(all) != 3 {
		t.Fatalf("full windows = %d, want 3", len(all))
	}
	if all[2].Kind != "rate_limit" {
		t.Fatalf("third window kind = %q, want rate_limit", all[2].Kind)
	}
	if all[0].Kind != "plan" || all[1].Kind != "credit" {
		t.Fatalf("kinds = %q, %q; want plan, credit", all[0].Kind, all[1].Kind)
	}
	if all[0].ResetsAt == "" {
		t.Fatal("plan window lost its reset time")
	}
	if all[1].ResetsAt != "" {
		t.Fatal("a window without a reset time was given one")
	}
}

// No agent, or a provider that reports nothing, is a normal answer — HasData
// false — not an error. Refreshable still rides along, so a client knows
// whether /usage should show a loading state before any data arrives.
func TestUsageInfoNoDataKeepsRefreshable(t *testing.T) {
	info := usageInfo(provider.UsageSnapshot{}, false, true)
	if info.HasData {
		t.Fatal("ok=false produced HasData")
	}
	if !info.Refreshable {
		t.Fatal("refreshable dropped when there is no data yet")
	}
	if len(info.Windows) != 0 || info.Credits != nil {
		t.Fatal("ok=false produced content")
	}
}

func TestUsageInfoCarriesCreditsAndCapturedAt(t *testing.T) {
	captured := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	snap := provider.UsageSnapshot{
		Provider:   "openrouter",
		Windows:    sampleWindows(),
		CapturedAt: captured,
		Credits:    &provider.Credits{HasCredits: true, Balance: 4.5, Used: 1.5},
	}
	info := usageInfo(snap, true, false)

	if !info.HasData || info.Provider != "openrouter" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Windows) != 3 {
		t.Fatalf("windows = %d, want all 3", len(info.Windows))
	}
	if info.CapturedAt != ctrlTimeString(captured) {
		t.Fatalf("capturedAt = %q", info.CapturedAt)
	}
	if info.Credits == nil || info.Credits.Balance != 4.5 || info.Credits.Used != 1.5 || !info.Credits.HasCredits {
		t.Fatalf("credits = %+v", info.Credits)
	}
}
