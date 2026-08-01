package modes

// The /context modal and its size-breakdown computation. The breakdown
// answers "what is filling my context window?" — terva has no tokenizer,
// so sizes are bytes and token counts are ~bytes/4 estimates, which is
// plenty to finger the culprit (usually one oversized tool result).

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// slashContext opens the /context modal: an Overview tab with the size
// breakdown and an Extensions tab with the full injected text. Replaces the
// old inline note so the breakdown has room and can scroll.
func (i *Interactive) slashContext() {
	sessionPath := ""
	if i.cfg.CurrentSessionPath != nil {
		sessionPath = i.cfg.CurrentSessionPath()
	}
	sessionID := ""
	if sessionPath != "" {
		sessionID = strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	}
	i.contextDialog.Open(sessionID, sessionPath,
		i.buildContextOverview(i.cfg.Theme),
		i.buildContextExtensions(i.cfg.Theme))
	i.invalidate()
}

// buildContextExtensions renders the per-extension injected context (the
// transparency view) as styled body lines for the Extensions tab.
func (i *Interactive) buildContextExtensions(th tui.Theme) []string {
	if i.cfg.Extensions == nil {
		if i.cfg.Carrier != nil {
			// Extensions run daemon-side in ctrlproto mode; their injected-text
			// detail has no surface yet (the byte totals are in the Overview).
			return []string{th.FG256(th.Muted, "  "+i18n.T("extension context detail is not available over the control plane yet"))}
		}
		return []string{th.FG256(th.Muted, "  "+i18n.T("extensions are not enabled"))}
	}
	items := i.cfg.Extensions.ContextSnapshot()
	if len(items) == 0 {
		return []string{th.FG256(th.Muted, "  "+i18n.T("no extension is contributing context to the model"))}
	}
	var out []string
	for _, it := range items {
		head := it.Source
		if it.Kind == "static" {
			head = i18n.T("%s (system guidance)", head)
		} else {
			label := it.Label
			if label == "" {
				label = it.ID
			}
			head = i18n.T("%s (card %q)", head, label)
		}
		out = append(out, th.FG256(th.Accent, "  "+head))
		for _, line := range strings.Split(strings.TrimRight(it.Text, "\n"), "\n") {
			out = append(out, th.FG256(th.Muted, "    "+line))
		}
	}
	return out
}

// buildContextOverview renders the size breakdown of everything that rides
// the next request: system prompt, tool defs, extension ephemeral context,
// and the transcript per message (with the largest flagged).
func (i *Interactive) buildContextOverview(th tui.Theme) []string {
	if i.cfg.Carrier == nil {
		return []string{th.FG256(th.Muted, "  "+i18n.T("no agent running"))}
	}
	// The daemon computes the breakdown (Context is a first-class service
	// verb); the renderer is shared with the web panel.
	bd, err := i.cfg.Carrier.Context(context.Background(), i.carrierSession())
	if err != nil {
		return []string{th.FG256(th.Muted, "  "+err.Error())}
	}
	return renderContextOverview(th, bd)
}

// renderContextOverview paints the Overview tab from a wire-shaped breakdown.
// The service is now its only data source — the in-process assembly it used to
// share this renderer with is gone.
func renderContextOverview(th tui.Theme, b ctrlproto.ContextBreakdown) []string {
	muted := func(s string) string { return th.FG256(th.Muted, s) }
	row := func(label string, bytes int, suffix string) string {
		return muted(fmt.Sprintf("  %-15s %10s  %-9s%s", label, humanBytes(bytes), estTok(bytes), suffix))
	}

	largestIdx, largestBytes := -1, 0
	for _, m := range b.Messages {
		if m.Bytes > largestBytes {
			largestBytes = m.Bytes
			largestIdx = m.Index
		}
	}

	var out []string
	sysSuffix := ""
	if b.ExtGuidanceBytes > 0 {
		sysSuffix = "  " + i18n.T("(incl. ext guidance)")
	}
	out = append(out, row(i18n.T("system prompt"), b.SystemBytes, sysSuffix))
	if b.ExtGuidanceBytes > 0 {
		out = append(out, muted("    "+i18n.T("└ of which ext guidance: %s (%s)",
			humanBytes(b.ExtGuidanceBytes), estTok(b.ExtGuidanceBytes))))
	}
	toolSuffix := "  " + i18n.T("[%d tools]", b.ToolCount)
	if b.ToolCountInstalled > b.ToolCount {
		// Lazy visibility: report the advertised set (what's on the wire) and the
		// installed total that would load if every group were activated.
		toolSuffix = "  " + i18n.T("[%d of %d tools · %s installed]",
			b.ToolCount, b.ToolCountInstalled, humanBytes(b.ToolBytesInstalled))
	}
	out = append(out, row(i18n.T("tool defs"), b.ToolBytes, toolSuffix))
	out = append(out, row(i18n.T("ext context"), b.ExtBytes, "  "+i18n.T("(cards, ephemeral)")))
	if b.LazyNoteBytes > 0 {
		out = append(out, muted("    "+i18n.T("└ of which lazy-tool note: %s (%s)",
			humanBytes(b.LazyNoteBytes), estTok(b.LazyNoteBytes))))
	}
	out = append(out, row(i18n.T("transcript"), b.TranscriptBytes, "  "+i18n.T("[%d msgs]", len(b.Messages))))
	for _, m := range b.Messages {
		line := fmt.Sprintf("    [%d] %-13s %10s", m.Index, m.Kind, humanBytes(m.Bytes))
		if m.Index == largestIdx && len(b.Messages) > 1 {
			out = append(out, th.FG256(th.Warning, line+"  "+i18n.T("← largest")))
		} else {
			out = append(out, muted(line))
		}
	}
	out = append(out, muted("  "+strings.Repeat("─", 38)))
	pctSuffix := ""
	if b.Window > 0 {
		pct := float64(b.TotalBytes) / float64(b.Window*4) * 100 // total/4 ~ tokens; window in tokens
		pctSuffix = "  " + i18n.T("(%.0f%% of %s window)", pct, humanCount(b.Window))
	}
	out = append(out, row(i18n.T("TOTAL"), b.TotalBytes, pctSuffix))
	out = append(out, "")
	out = append(out, muted("  "+i18n.T("sizes are bytes; token counts are ~bytes/4 estimates")))
	out = append(out, renderCacheSection(th, b.Cache)...)
	return out
}

// sparkLevels are the eight bar heights a hit rate quantizes to. Eight is what
// the block-element range gives; a rate is a fraction, so the mapping is exact
// enough that the shape is the signal and nobody reads a height as a number.
var sparkLevels = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// renderCacheSection paints the prompt-cache reading below the size breakdown.
//
// It sits under the byte estimates on purpose, and reads as their counterweight:
// everything above is terva guessing what it is about to send, this is the
// provider reporting what it actually read and what it charged for. The two
// disagreeing is informative — a big transcript with a high hit rate costs
// almost nothing, which the byte total alone would never tell you.
func renderCacheSection(th tui.Theme, c *ctrlproto.ContextCache) []string {
	muted := func(s string) string { return th.FG256(th.Muted, s) }
	if c == nil {
		// A daemon older than this field. Say nothing rather than "0%".
		return nil
	}
	out := []string{"", muted("  " + strings.Repeat("─", 38))}

	if !c.Supported {
		if c.Session.Input+c.Session.CacheRead+c.Session.CacheWrite == 0 {
			return append(out, muted("  "+i18n.T("prompt cache")+"    "+i18n.T("no requests yet")))
		}
		// Real traffic, no cache reported: an endpoint without a prefix cache, or
		// prompts under its minimum cacheable size. Not a 0% hit rate — there is
		// nothing here to be missing.
		return append(out, muted("  "+i18n.T("prompt cache")+"    "+
			i18n.T("this provider reported no cache activity")))
	}

	sessRate, _ := usageRate(c.Session)
	head := fmt.Sprintf("  %-15s %s", i18n.T("prompt cache"),
		th.FG256(th.MeterColor(100-sessRate*100), fmt.Sprintf("%3.0f%% ", sessRate*100)+i18n.T("hit")))
	if saved := c.Session.CacheSavedUSD; saved != 0 {
		// Sign it in words, not with a minus buried in a currency. A session that
		// keeps rewriting a prefix it never reads back costs MORE than no cache,
		// and "-$0.42 saved" is a sentence people read as a saving.
		if saved > 0 {
			head += muted(fmt.Sprintf("   %s $%.2f", i18n.T("saved"), saved))
		} else {
			head += th.FG256(th.Warning, fmt.Sprintf("   %s $%.2f", i18n.T("cost extra"), -saved))
		}
	}
	out = append(out, head)

	if last := c.LastRequest; last.Input+last.CacheRead+last.CacheWrite > 0 {
		rate, _ := usageRate(last)
		parts := []string{i18n.T("%s read", humanCount(last.CacheRead))}
		if last.CacheWrite > 0 {
			parts = append(parts, i18n.T("%s written", humanCount(last.CacheWrite)))
		}
		parts = append(parts, i18n.T("%s fresh", humanCount(last.Input)))
		out = append(out, muted(fmt.Sprintf("    %-13s %s  ", i18n.T("last request"),
			strings.Join(parts, " · ")))+
			th.FG256(th.MeterColor(100-rate*100), fmt.Sprintf("(%.0f%%)", rate*100)))
	}

	if len(c.Recent) > 1 {
		out = append(out, muted(fmt.Sprintf("    %-13s ", i18n.T("last %d", len(c.Recent))))+
			cacheSpark(th, c.Recent))
	}
	return out
}

// cacheSpark draws one cell per recent request, height and colour by hit rate.
//
// Per-request rather than averaged because the average is the one thing already
// on the line above. What this adds is WHERE the cache broke: a prefix change
// shows up as a single notch in an otherwise full bar, and that notch is the
// whole diagnosis — it dates the invalidation to a request, which is what makes
// it possible to remember what was changed just before it.
func cacheSpark(th tui.Theme, recent []ctrlproto.CacheSample) string {
	var sb strings.Builder
	for _, s := range recent {
		rate := s.HitRate
		if rate < 0 {
			rate = 0
		} else if rate > 1 {
			rate = 1
		}
		level := int(rate * float64(len(sparkLevels)))
		if level >= len(sparkLevels) {
			level = len(sparkLevels) - 1
		}
		sb.WriteString(th.FG256(th.MeterColor(100-rate*100), string(sparkLevels[level])))
	}
	return sb.String()
}

// usageRate is CacheHitRate over the wire shape, so the renderers do not each
// re-derive the denominator. Same definition as provider.Usage.CacheHitRate:
// cache reads over the whole prompt, cached and not.
func usageRate(u core.WireUsage) (rate float64, ok bool) {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	if prompt <= 0 {
		return 0, false
	}
	return float64(u.CacheRead) / float64(prompt), true
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func estTok(bytes int) string { return "~" + humanCount(bytes/4) + " tok" }

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}
