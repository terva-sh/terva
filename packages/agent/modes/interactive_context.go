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
	return out
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
