package modes

// The /context modal and its size-breakdown computation. The breakdown
// answers "what is filling my context window?" — terva has no tokenizer,
// so sizes are bytes and token counts are ~bytes/4 estimates, which is
// plenty to finger the culprit (usually one oversized tool result).

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
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
	ag := i.turns.Agent()
	if ag == nil {
		return []string{th.FG256(th.Muted, "  "+i18n.T("no agent running"))}
	}
	muted := func(s string) string { return th.FG256(th.Muted, s) }

	sysBytes := len(ag.System)
	toolBytes := 0
	if specs := ag.Tools.Specs(); len(specs) > 0 {
		if b, err := json.Marshal(specs); err == nil {
			toolBytes = len(b)
		}
	}
	toolCount := len(ag.Tools)
	// Size the per-turn tail with the side-effect-free twin when present, so
	// opening /context never overwrites the "fired last turn" lore record (a
	// re-scan of the now-longer transcript would report lore that never fired).
	ephBytes := 0
	if sizer := ag.ContextProviderPeek; sizer != nil {
		ephBytes = len(sizer())
	} else if ag.ContextProvider != nil {
		ephBytes = len(ag.ContextProvider())
	}
	// Extensions contribute in two places: "static" guidance is folded into
	// the system prompt (so it's already inside sysBytes), while "card"
	// context rides the ephemeral block (ephBytes). Surface the static share
	// separately so "ext context" reading a tiny number isn't mistaken for
	// "extensions inject almost nothing" — the bulk is usually the guidance,
	// counted under the system prompt.
	extStaticBytes := 0
	if i.cfg.Extensions != nil {
		for _, it := range i.cfg.Extensions.ContextSnapshot() {
			if it.Kind == "static" {
				extStaticBytes += len(it.Text)
			}
		}
	}

	msgs := ag.Messages()
	transcriptBytes := 0
	largestIdx, largestBytes := -1, 0
	perMsg := make([]int, len(msgs))
	for idx, m := range msgs {
		b := messageBytes(m)
		perMsg[idx] = b
		transcriptBytes += b
		if b > largestBytes {
			largestBytes = b
			largestIdx = idx
		}
	}
	total := sysBytes + toolBytes + ephBytes + transcriptBytes

	window := 0
	if mdl, err := provider.FindModel("", ag.Model); err == nil {
		window = mdl.ContextWindow
	}

	row := func(label string, bytes int, suffix string) string {
		return muted(fmt.Sprintf("  %-15s %10s  %-9s%s", label, humanBytes(bytes), estTok(bytes), suffix))
	}

	var out []string
	sysSuffix := ""
	if extStaticBytes > 0 {
		sysSuffix = "  " + i18n.T("(incl. ext guidance)")
	}
	out = append(out, row(i18n.T("system prompt"), sysBytes, sysSuffix))
	if extStaticBytes > 0 {
		out = append(out, muted("    "+i18n.T("└ of which ext guidance: %s (%s)",
			humanBytes(extStaticBytes), estTok(extStaticBytes))))
	}
	out = append(out, row(i18n.T("tool defs"), toolBytes, "  "+i18n.T("[%d tools]", toolCount)))
	out = append(out, row(i18n.T("ext context"), ephBytes, "  "+i18n.T("(cards, ephemeral)")))
	out = append(out, row(i18n.T("transcript"), transcriptBytes, "  "+i18n.T("[%d msgs]", len(msgs))))
	for idx, m := range msgs {
		line := fmt.Sprintf("    [%d] %-13s %10s", idx, messageKind(m), humanBytes(perMsg[idx]))
		if idx == largestIdx && len(msgs) > 1 {
			out = append(out, th.FG256(th.Warning, line+"  "+i18n.T("← largest")))
		} else {
			out = append(out, muted(line))
		}
	}
	out = append(out, muted("  "+strings.Repeat("─", 38)))
	pctSuffix := ""
	if window > 0 {
		pct := float64(total) / float64(window*4) * 100 // total/4 ~ tokens; window in tokens
		pctSuffix = "  " + i18n.T("(%.0f%% of %s window)", pct, humanCount(window))
	}
	out = append(out, row(i18n.T("TOTAL"), total, pctSuffix))
	out = append(out, "")
	out = append(out, muted("  "+i18n.T("sizes are bytes; token counts are ~bytes/4 estimates")))
	return out
}

// messageBytes estimates one message's wire size by marshalling it to JSON
// (close to what a provider serialises, and accounts for tool results +
// base64 image data). Falls back to summing text content.
func messageBytes(m provider.Message) int {
	if b, err := json.Marshal(m); err == nil {
		return len(b)
	}
	n := 0
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			n += len(tb.Text)
		}
	}
	return n
}

// messageKind labels a message by role, distinguishing tool results / calls
// / images so an oversized tool result stands out in the breakdown. A
// compaction summary (a synthetic user message left by Compact) is labelled
// "compaction" so it's visible that the stack restarts there: compaction
// replaces the transcript with [summary + kept tail], so the breakdown shows
// the post-compaction transcript, not the messages it folded away.
func messageKind(m provider.Message) string {
	if m.Meta["compaction"] == "true" {
		return "compaction"
	}
	role := string(m.Role)
	for _, c := range m.Content {
		switch c.(type) {
		case provider.ToolResultBlock:
			return "tool_result"
		case provider.ToolCallBlock:
			return role + "+tool"
		case provider.ImageBlock:
			return role + "+image"
		}
	}
	return role
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
