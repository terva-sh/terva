package modes

import (
	"testing"
	"time"

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

// The mid-turn refresh coalesces: per-step usage events arrive several times a
// second in a tool-heavy turn, and only the first inside the window may kick a
// carrier round-trip. A nil Carrier makes the spawned fetch a no-op, so the
// test observes the gate through the stamp alone.
func TestRefreshUsageThrottledCoalesces(t *testing.T) {
	i := &Interactive{}

	i.mu.Lock()
	i.refreshUsageThrottledLocked()
	first := i.carrierUsageFetched
	i.refreshUsageThrottledLocked()
	second := i.carrierUsageFetched
	i.mu.Unlock()

	if first.IsZero() {
		t.Fatal("first throttled refresh did not stamp")
	}
	if !second.Equal(first) {
		t.Error("second refresh inside the window restamped (throttle did not coalesce)")
	}

	// Past the window, the next event refreshes again.
	i.mu.Lock()
	i.carrierUsageFetched = time.Now().Add(-usageRefreshMinInterval - time.Second)
	i.refreshUsageThrottledLocked()
	third := i.carrierUsageFetched
	i.mu.Unlock()
	if !third.After(first) {
		t.Error("refresh past the window did not restamp")
	}
}

// The unthrottled paths (turn-over, binding, /usage) stamp too, so a done
// refresh suppresses an immediately following per-step refresh instead of
// doubling the round-trip.
func TestRefreshUsageAsyncStampsThrottle(t *testing.T) {
	i := &Interactive{}
	i.refreshUsageAsync(false)

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.carrierUsageFetched.IsZero() {
		t.Fatal("refreshUsageAsync did not stamp the throttle window")
	}
	stamp := i.carrierUsageFetched
	i.refreshUsageThrottledLocked()
	if !i.carrierUsageFetched.Equal(stamp) {
		t.Error("throttled refresh fired right after an unthrottled one")
	}
}
