package modes

// The /context modal and its size-breakdown computation. The breakdown
// answers "what is filling my context window?" — terva has no tokenizer,
// so sizes are bytes and token counts are ~bytes/4 estimates, which is
// plenty to finger the culprit (usually one oversized tool result).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
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
	// ctrlproto mode: the daemon computes the breakdown (Context is a
	// first-class service verb); the renderer below is shared.
	if i.cfg.Carrier != nil {
		bd, err := i.cfg.Carrier.Context(context.Background(), i.carrierSession())
		if err != nil {
			return []string{th.FG256(th.Muted, "  "+err.Error())}
		}
		return renderContextOverview(th, bd)
	}
	ag := i.turns.Agent()
	if ag == nil {
		return []string{th.FG256(th.Muted, "  "+i18n.T("no agent running"))}
	}
	return renderContextOverview(th, i.assembleContextBreakdown(ag))
}

// assembleContextBreakdown builds the /context numbers from the in-process
// agent — the legacy twin of the Workspace's contextBreakdown, feeding the
// same renderer so both paths paint identically.
func (i *Interactive) assembleContextBreakdown(ag *core.Agent) ctrlproto.ContextBreakdown {
	var b ctrlproto.ContextBreakdown
	b.SystemBytes = len(ag.System)
	if specs := ag.Tools.Specs(); len(specs) > 0 {
		if raw, err := json.Marshal(specs); err == nil {
			b.ToolBytes = len(raw)
		}
	}
	b.ToolCount = len(ag.Tools)
	// Size the per-turn tail with the side-effect-free twin when present, so
	// opening /context never overwrites the "fired last turn" lore record (a
	// re-scan of the now-longer transcript would report lore that never fired).
	if sizer := ag.ContextProviderPeek; sizer != nil {
		b.ExtBytes = len(sizer())
	} else if ag.ContextProvider != nil {
		b.ExtBytes = len(ag.ContextProvider())
	}
	// Extensions contribute in two places: "static" guidance is folded into
	// the system prompt (so it is already inside SystemBytes), while "card"
	// context rides the ephemeral block (ExtBytes). Surface the static share
	// separately so "ext context" reading a tiny number is not mistaken for
	// "extensions inject almost nothing" — the bulk is usually the guidance,
	// counted under the system prompt.
	if i.cfg.Extensions != nil {
		for _, it := range i.cfg.Extensions.ContextSnapshot() {
			if it.Kind == "static" {
				b.ExtGuidanceBytes += len(it.Text)
			}
		}
	}
	msgs := ag.Messages()
	b.Messages = make([]ctrlproto.ContextMessage, len(msgs))
	for idx, m := range msgs {
		n := messageBytes(m)
		b.Messages[idx] = ctrlproto.ContextMessage{Index: idx, Kind: messageKind(m), Bytes: n}
		b.TranscriptBytes += n
	}
	b.TotalBytes = b.SystemBytes + b.ToolBytes + b.ExtBytes + b.TranscriptBytes
	if mdl, err := provider.FindModel("", ag.Model); err == nil {
		b.Window = mdl.ContextWindow
	}
	return b
}

// renderContextOverview paints the Overview tab from a wire-shaped breakdown —
// the one renderer both the legacy (local agent) and ctrlproto (service) data
// sources feed.
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
	out = append(out, row(i18n.T("tool defs"), b.ToolBytes, "  "+i18n.T("[%d tools]", b.ToolCount)))
	out = append(out, row(i18n.T("ext context"), b.ExtBytes, "  "+i18n.T("(cards, ephemeral)")))
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
