package modes

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// contextDialog is the /context modal: a tabbed, scrollable view of what's
// in the model's context window right now.
//
//   - Overview: a size breakdown (system prompt, tool defs, extension
//     context, per-message transcript + total/% of window) so a surprise
//     context bloat is traceable to its source.
//   - Extensions: the full text each extension injects (the transparency
//     view that used to print inline via /context).
//
// Both bodies are pre-rendered, styled lines frozen at Open time; the
// dialog only switches tabs and scrolls.
type contextDialog struct {
	active      bool
	tab         int // 0 = overview, 1 = extensions
	scroll      int
	sessionID   string
	sessionPath string
	overview    []string
	exts        []string
}

const (
	contextTabCount = 2
	contextBodyRows = 14
)

func newContextDialog() *contextDialog { return &contextDialog{} }

func (d *contextDialog) Active() bool { return d != nil && d.active }

// Open freezes a snapshot of the breakdown and extension context.
func (d *contextDialog) Open(sessionID, sessionPath string, overview, exts []string) {
	d.active = true
	d.tab = 0
	d.scroll = 0
	d.sessionID = sessionID
	d.sessionPath = sessionPath
	d.overview = overview
	d.exts = exts
}

func (d *contextDialog) Close() {
	d.active = false
	d.overview = nil
	d.exts = nil
	d.scroll = 0
	d.tab = 0
}

func (d *contextDialog) body() []string {
	if d.tab == 1 {
		return d.exts
	}
	return d.overview
}

// wrappedBody folds the active tab's lines to the dialog width so a long
// line (e.g. an extension's injected text, which arrives as one logical
// line) wraps and stays scrollable instead of running off the right edge.
// Wrapping is width-aware and preserves the ANSI colour of each line.
func (d *contextDialog) wrappedBody(width int) []string {
	limit := max(width-2, 24)
	src := d.body()
	out := make([]string, 0, len(src))
	for _, line := range src {
		if line == "" {
			out = append(out, "")
			continue
		}
		// WrapANSILineKeepStyle re-applies the line's colour to each wrapped
		// row, so an extension-context line that folds stays one consistent
		// colour (each rendered row resets SGR independently — see
		// paintBackgroundRow). The body lines are pre-styled and emitted
		// directly, which is exactly what keep-style is for.
		out = append(out, tui.WrapANSILineKeepStyle(line, limit)...)
	}
	return out
}

// HandleKey routes a key while the dialog owns the screen: Tab / ←→ switch
// tabs, ↑/↓/PgUp/PgDn scroll, esc closes (closed=true).
func (d *contextDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return true
	case tui.KeyTab, tui.KeyRight:
		d.tab = (d.tab + 1) % contextTabCount
		d.scroll = 0
	case tui.KeyShiftTab, tui.KeyLeft:
		d.tab = (d.tab + contextTabCount - 1) % contextTabCount
		d.scroll = 0
	case tui.KeyUp:
		if d.scroll > 0 {
			d.scroll--
		}
	case tui.KeyDown:
		d.scroll++ // clamped against the body length in Render
	case tui.KeyPageUp:
		d.scroll -= contextBodyRows
		if d.scroll < 0 {
			d.scroll = 0
		}
	case tui.KeyPageDown:
		d.scroll += contextBodyRows
	}
	return false
}

func (d *contextDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, i18n.T("context"), width))
	lines = append(lines, d.tabBar(th))
	if d.sessionID != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("session %s", d.sessionID)))
	}
	if d.sessionPath != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+truncate(d.sessionPath, width-4)))
	}
	lines = append(lines, "")

	body := d.wrappedBody(width)
	maxScroll := len(body) - contextBodyRows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if d.scroll > maxScroll {
		d.scroll = maxScroll
	}
	start := d.scroll
	end := start + contextBodyRows
	if end > len(body) {
		end = len(body)
	}
	if start > 0 {
		lines = append(lines, windowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		lines = append(lines, body[i])
	}
	if end < len(body) {
		lines = append(lines, windowMoreBelow(th, len(body), end))
	}
	lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("←→/tab switch · ↑/↓ scroll · esc")))
	lines = append(lines, frameRule(th, width))
	return lines
}

func (d *contextDialog) tabBar(th tui.Theme) string {
	names := []string{"Overview", "Extensions"}
	parts := make([]string, 0, len(names))
	for idx, n := range names {
		if idx == d.tab {
			parts = append(parts, th.FG256(th.Accent, "["+n+"]"))
		} else {
			parts = append(parts, th.FG256(th.Muted, " "+n+" "))
		}
	}
	return "  " + strings.Join(parts, " ")
}
