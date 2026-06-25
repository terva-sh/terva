package modes

// Host glue for /usage and the status-bar usage hint. Unlike most
// dialogs, /usage needs no InteractiveConfig callback: the usage
// snapshot lives on the provider client, which the modes layer already
// reaches through the live *core.Agent it is driving (turns.Agent()).
// Reading it there means a /model swap to another provider transparently
// surfaces that provider's reporter — or none — with no extra wiring.

import (
	"context"
	"time"

	"terva.sh/terva/packages/provider"
)

// openUsageDialog shows the /usage modal for the current provider. It always
// opens: when the provider reports no usage, the dialog renders a "doesn't
// report usage limits" line rather than failing. For a poll-based provider
// (OpenRouter, DeepSeek) it opens immediately with whatever is cached, then
// fetches in the background and updates the open dialog when the result lands.
func (i *Interactive) openUsageDialog() {
	snap, ok := i.currentUsage()
	loading := false
	if ag := i.turns.Agent(); ag != nil && ag.UsageRefreshable() {
		// Only show "fetching…" when we have nothing cached to render yet.
		loading = !ok
		i.refreshUsageAsync()
	}
	i.usageDialog.Open(i.cfg.Provider, snap, ok, loading)
	i.invalidate()
}

// refreshUsageAsync fetches the provider's usage off the UI goroutine and
// applies the result to the open /usage dialog. A no-op for providers that
// report nothing; for codex (header-based) RefreshUsage returns instantly.
func (i *Interactive) refreshUsageAsync() {
	ag := i.turns.Agent()
	if ag == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		snap, ok := ag.RefreshUsage(ctx)
		i.runOnMain(func() {
			if i.usageDialog.Active() {
				i.usageDialog.Update(snap, ok)
				i.invalidate()
			}
		})
	}()
}

// currentUsage reads the live agent's usage snapshot, ok=false when there
// is no agent yet or the provider reports none.
func (i *Interactive) currentUsage() (provider.UsageSnapshot, bool) {
	ag := i.turns.Agent()
	if ag == nil {
		return provider.UsageSnapshot{}, false
	}
	return ag.Usage()
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
