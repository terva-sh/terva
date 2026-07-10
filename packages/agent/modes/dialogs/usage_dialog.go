package dialogs

// usageDialog is the /usage modal: a read-only view of where the user
// stands against their subscription's usage windows (and credits, when
// the provider reports them). It mutates nothing — Open snapshots the
// current state, Render formats it, esc closes. Most providers report
// no usage; the dialog says so plainly rather than hiding the command.

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

const usageBarCells = 14

type UsageDialog struct {
	active     bool
	providerID string // current provider id, the fallback display name
	snapshot   provider.UsageSnapshot
	hasData    bool
	loading    bool // a background usage fetch is in flight
}

func NewUsageDialog() *UsageDialog { return &UsageDialog{} }

func (d *UsageDialog) Active() bool { return d != nil && d.active }

// Open snapshots the current usage. providerID is the live provider id
// (i.cfg.Provider), used for the "doesn't report usage" line when the
// snapshot itself is empty. loading=true (with no cached data) renders a
// "fetching…" line until Update arrives.
func (d *UsageDialog) Open(providerID string, snap provider.UsageSnapshot, ok, loading bool) {
	d.active = true
	d.providerID = providerID
	d.snapshot = snap
	d.hasData = ok
	d.loading = loading
}

// Update applies a freshly-fetched snapshot to the open dialog and clears the
// loading state. Called on the main goroutine after a background refresh.
func (d *UsageDialog) Update(snap provider.UsageSnapshot, ok bool) {
	d.snapshot = snap
	d.hasData = ok
	d.loading = false
}

func (d *UsageDialog) Close() {
	d.active = false
	d.snapshot = provider.UsageSnapshot{}
	d.hasData = false
	d.loading = false
}

func (d *UsageDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}
	if k.Kind == tui.KeyEsc {
		d.Close()
		return true
	}
	return false
}

func (d *UsageDialog) providerLabel() string {
	if d.snapshot.Provider != "" {
		return d.snapshot.Provider
	}
	if d.providerID != "" {
		return d.providerID
	}
	return "this provider"
}

func (d *UsageDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	lines := []string{FrameHeader(th, i18n.T("usage"), width), ""}

	if d.loading && !d.hasData {
		lines = append(lines,
			th.FG256(th.Muted, "  "+i18n.T("fetching usage…")),
			"",
			th.FG256(th.Muted, "  "+i18n.T("esc")),
			FrameRule(th, width),
		)
		return lines
	}

	if !d.hasData || (len(d.snapshot.Windows) == 0 && d.snapshot.Credits == nil) {
		lines = append(lines,
			th.FG256(th.Muted, "  "+i18n.T("%s doesn't report usage limits.", d.providerLabel())),
			"",
			th.FG256(th.Muted, "  "+i18n.T("esc")),
			FrameRule(th, width),
		)
		return lines
	}

	lines = append(lines, th.FG256(th.Muted, "  "+d.providerLabel()), "")
	now := time.Now()
	rateHeaderShown := false
	for _, w := range d.snapshot.Windows {
		// Windows arrive ordered plan/credit before rate-limit (mergeUsage);
		// label the rate-limit group once so the two kinds read apart.
		if w.Kind == provider.WindowRateLimit && !rateHeaderShown {
			lines = append(lines, "", th.FG256(th.Muted, "  "+i18n.T("rate limits")))
			rateHeaderShown = true
		}
		lines = append(lines, renderUsageWindow(th, w, now))
	}
	if d.snapshot.Credits != nil {
		lines = append(lines, "", "  "+formatCredits(th, d.snapshot.Credits))
	}
	lines = append(lines, "", th.FG256(th.Muted, "  esc"), FrameRule(th, width))
	return lines
}

// renderUsageWindow formats one window as "  label [bar] pct  resets in …".
// now is passed in so the reset countdown is testable without a clock.
func renderUsageWindow(th tui.Theme, w provider.UsageWindow, now time.Time) string {
	color := usageColor(th, w.UsedPercent)
	pct := "   ? "
	if w.UsedPercent >= 0 {
		pct = fmt.Sprintf("%4.0f%%", w.UsedPercent)
	}
	line := "  " + th.FG256(th.Muted, fmt.Sprintf("%-8s", w.Label)) +
		" " + th.FG256(color, usageBar(w.UsedPercent, usageBarCells)) +
		" " + th.FG256(color, pct)
	if reset := formatReset(w.ResetsAt, now); reset != "" {
		line += th.FG256(th.Muted, "  "+reset)
	}
	return line
}

func usageBar(pct float64, cells int) string {
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	filled := min(int(pct/100*float64(cells)+0.5), cells)
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", cells-filled) + "]"
}

// usageColor mirrors the status bar's context-percentage thresholds:
// calm (accent) below 70, warning to 90, error above.
func usageColor(th tui.Theme, pct float64) int {
	switch {
	case pct >= 90:
		return th.Error
	case pct >= 70:
		return th.Warning
	default:
		return th.Accent
	}
}

// formatReset humanizes the time until a window rolls over. Empty when
// the reset is unknown (zero time); "resets now" once it's due.
func formatReset(resetsAt, now time.Time) string {
	if resetsAt.IsZero() {
		return ""
	}
	d := resetsAt.Sub(now)
	if d <= 0 {
		return "resets now"
	}
	return "resets in " + humanizeDuration(d)
}

func humanizeDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		if hrs := int((d % (24 * time.Hour)) / time.Hour); hrs > 0 {
			return fmt.Sprintf("%dd%dh", days, hrs)
		}
		return fmt.Sprintf("%dd", days)
	case d >= time.Hour:
		h := int(d / time.Hour)
		if m := int((d % time.Hour) / time.Minute); m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	default:
		if m := int(d / time.Minute); m >= 1 {
			return fmt.Sprintf("%dm", m)
		}
		return "<1m"
	}
}

func formatCredits(th tui.Theme, c *provider.Credits) string {
	switch {
	case c.Unlimited:
		return th.FG256(th.Muted, i18n.T("credits: unlimited"))
	case c.HasCredits || c.Balance > 0:
		// Remaining is the headline; append spend when the provider reports it.
		s := i18n.T("credits: %.2f remaining", c.Balance)
		if c.Used > 0 {
			s += "  " + i18n.T("(%.2f used)", c.Used)
		}
		return th.FG256(th.Muted, s)
	case c.Used > 0:
		// No remaining figure (e.g. an uncapped OpenRouter key) — show spend.
		return th.FG256(th.Muted, i18n.T("credits: %.2f used", c.Used))
	default:
		return th.FG256(th.Muted, i18n.T("credits: none"))
	}
}
