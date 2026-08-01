package modes

// Host glue for /usage and the status-bar usage hint.
//
// The provider's subscription picture — plan and rate-limit windows, credits,
// a captured-at — lives on the provider client, which is the daemon's. The TUI
// mirrors it from the usage.snapshot verb rather than reaching through a live
// *core.Agent: refreshed once per turn (a cached read), once per binding, and
// on demand when /usage opens.
//
// statusUsageWindows runs on every frame, so it reads the mirror and never the
// wire. Filtering is the client's business: the status bar drops the ephemeral
// rate-limit windows (they would churn an always-visible meter) and the modal
// shows them, so the wire carries all of them.

import (
	"context"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// usageFetchTimeout bounds the refresh round-trip. The daemon may be talking to
// the provider's usage endpoint (OpenRouter, DeepSeek) on the other side.
const usageFetchTimeout = 8 * time.Second

// fetchCarrierUsage refreshes the usage mirror. refresh=true asks the daemon to
// pull from the provider's endpoint, which BLOCKS on it — so this must run off
// the UI goroutine. It is safe for every provider: the daemon falls back to the
// passively-observed snapshot for clients that have no endpoint.
func (i *Interactive) fetchCarrierUsage(refresh bool) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil || sess == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageFetchTimeout)
	defer cancel()
	info, err := c.UsageSnapshot(ctx, sess, refresh)
	if err != nil {
		// A usage read is decorative; a failed one must not raise a banner on
		// every turn. The mirror keeps whatever it had.
		return
	}
	i.mu.Lock()
	i.carrierUsage = info
	i.mu.Unlock()
	i.runOnMain(func() {
		if i.usageDialog.Active() {
			snap, ok := i.currentUsage()
			i.usageDialog.Update(snap, ok)
		}
		i.invalidate()
	})
}

// refreshUsageAsync kicks a refresh off the UI goroutine.
func (i *Interactive) refreshUsageAsync(refresh bool) {
	i.mu.Lock()
	i.carrierUsageFetched = time.Now()
	i.mu.Unlock()
	go i.fetchCarrierUsage(refresh)
}

// usageRefreshMinInterval is the mid-turn throttle floor: per-step usage
// events can arrive several times a second in a tool-heavy turn, and each
// mirror refresh is a carrier round-trip. Turn-over and binding refreshes
// bypass the throttle (refreshUsageAsync), so the meters are never more
// than one interval stale while a turn streams and are exact at its end.
const usageRefreshMinInterval = 5 * time.Second

// refreshUsageThrottledLocked refreshes the usage mirror unless one was
// kicked off within usageRefreshMinInterval. Called (with i.mu held) on
// per-step usage events so the status-bar meters appear during a
// long-running turn — a session whose first turn runs for minutes otherwise
// shows no subscription picture at all until the turn ends.
func (i *Interactive) refreshUsageThrottledLocked() {
	if time.Since(i.carrierUsageFetched) < usageRefreshMinInterval {
		return
	}
	i.carrierUsageFetched = time.Now()
	go i.fetchCarrierUsage(false)
}

// usageRefreshable reports whether the current provider fetches its usage from
// an endpoint — i.e. /usage should show a loading state rather than rendering
// instantly from headers. False until the first mirror refresh lands.
func (i *Interactive) usageRefreshable() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierUsage.Refreshable
}

// openUsageDialog shows the /usage modal for the current provider. It always
// opens: when the provider reports no usage, the dialog renders a "doesn't
// report usage limits" line rather than failing. It then refreshes in the
// background and updates the open dialog when the result lands.
//
// OPEN FIRST, then refresh. The order is load-bearing twice over, and used to
// be the other way round:
//
//   - fetchCarrierUsage's completion hop is gated on usageDialog.Active(). A
//     refresh that landed before Open ran therefore threw its result away, and
//     Open then rendered the snapshot captured above — from BEFORE the refresh
//     — with the loading flag computed from that same stale read. The daemon
//     answers instantly from cache for providers with no usage endpoint (the
//     comment on fetchCarrierUsage says so), which is exactly when that race
//     is winnable, and the dialog could sit on "fetching…" with the data
//     already in the mirror.
//   - UsageDialog carries no lock; runOnMain is what keeps it single-goroutine,
//     and runOnMain falls back to running inline when its action buffer is
//     saturated. Spawning the refresh before Open put that inline path in a
//     genuine data race with Open's writes — caught by -race, 3 hits in 500
//     runs. Creating the goroutine after Open orders the two.
func (i *Interactive) openUsageDialog() {
	snap, ok := i.currentUsage()
	// Only show "fetching…" when there is nothing cached to render yet AND the
	// provider is one that actually goes out to fetch.
	loading := !ok && i.usageRefreshable()
	i.usageDialog.Open(i.cfg.Provider, snap, ok, loading)
	i.refreshUsageAsync(true)
	i.invalidate()
}

// currentUsage reads the mirrored usage snapshot; ok=false before the first
// refresh lands, or when the provider reports none.
func (i *Interactive) currentUsage() (provider.UsageSnapshot, bool) {
	i.mu.Lock()
	info := i.carrierUsage
	i.mu.Unlock()
	if !info.HasData {
		return provider.UsageSnapshot{}, false
	}
	return usageSnapshotFromWire(info), true
}

// statusUsageWindows feeds the compact status-bar usage hint; nil when the
// current provider reports no plan/credit windows. Ephemeral rate-limit
// windows are dropped here — they would churn the always-visible bar — and
// remain visible in the /usage dialog.
func (i *Interactive) statusUsageWindows() []provider.UsageWindow {
	snap, ok := i.currentUsage()
	if !ok {
		return nil
	}
	return planWindows(snap.Windows)
}

// planWindows keeps the windows the status hint should show — plan and credit
// budgets, never rate-limit throughput windows.
func planWindows(ws []provider.UsageWindow) []provider.UsageWindow {
	var out []provider.UsageWindow
	for _, w := range ws {
		if w.Kind != provider.WindowRateLimit {
			out = append(out, w)
		}
	}
	return out
}

// usageSnapshotFromWire is the inverse of the workspace's usageInfo.
func usageSnapshotFromWire(info ctrlproto.UsageInfo) provider.UsageSnapshot {
	snap := provider.UsageSnapshot{Provider: info.Provider}
	if t, err := time.Parse(time.RFC3339, info.CapturedAt); err == nil {
		snap.CapturedAt = t
	}
	for _, w := range info.Windows {
		uw := provider.UsageWindow{
			Label:         w.Label,
			UsedPercent:   w.UsedPercent,
			WindowMinutes: w.WindowMinutes,
			Kind:          windowKindFromWire(w.Kind),
		}
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			uw.ResetsAt = t
		}
		snap.Windows = append(snap.Windows, uw)
	}
	if c := info.Credits; c != nil {
		snap.Credits = &provider.Credits{
			Unlimited:  c.Unlimited,
			HasCredits: c.HasCredits,
			Balance:    c.Balance,
			Used:       c.Used,
		}
	}
	return snap
}

func windowKindFromWire(kind string) provider.WindowKind {
	switch kind {
	case "credit":
		return provider.WindowCredit
	case "rate_limit":
		return provider.WindowRateLimit
	}
	return provider.WindowPlan
}
