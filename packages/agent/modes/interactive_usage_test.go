package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// Tests for the helpers in interactive_usage.go.

// The status-bar hint drops ephemeral rate-limit windows; plan/credit windows
// pass through. (The /usage dialog still shows everything.)
func TestPlanWindowsDropsRateLimit(t *testing.T) {
	ws := []provider.UsageWindow{
		{Label: "weekly", Kind: provider.WindowPlan},
		{Label: "requests", Kind: provider.WindowRateLimit},
		{Label: "tokens", Kind: provider.WindowRateLimit},
	}
	got := planWindows(ws)
	if len(got) != 1 || got[0].Label != "weekly" {
		t.Errorf("planWindows = %+v, want only the plan window", got)
	}
	if got := planWindows([]provider.UsageWindow{{Kind: provider.WindowRateLimit}}); got != nil {
		t.Errorf("all-rate-limit should yield nil (hint shows nothing), got %+v", got)
	}
}
