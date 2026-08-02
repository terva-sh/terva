package dialogs

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// ContextDialog is the /context modal: a tabbed, scrollable view of what's
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
type ContextDialog struct {
	active bool
	tab    int // 0 = overview, 1 = extensions
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to contextBodyRows.
	MaxRows     int
	sessionID   string
	sessionPath string
	overview    []string
	exts        []string
}

const (
	contextTabCount = 2
	contextBodyRows = 14
)

// bodyRows is the height to window the body to: the host's budget when it set
// one, else the standalone fallback.
func (d *ContextDialog) bodyRows() int {
	if d.MaxRows > 0 {
		return d.MaxRows
	}
	return contextBodyRows
}

// ChromeRows is the non-body rows Render emits at their WORST case: header, tab
// bar, session id, session path, the blank, both scroll indicators, the key
// hint and the closing rule. The two indicators and the two session lines are
// conditional, which is exactly why this is a maximum.
func (d *ContextDialog) ChromeRows() int { return 9 }

func NewContextDialog() *ContextDialog { return &ContextDialog{} }

func (d *ContextDialog) Active() bool { return d != nil && d.active }

// Open freezes a snapshot of the breakdown and extension context.
func (d *ContextDialog) Open(sessionID, sessionPath string, overview, exts []string) {
	d.active = true
	d.tab = 0
	d.vp.Reset()
	d.sessionID = sessionID
	d.sessionPath = sessionPath
	d.overview = overview
	d.exts = exts
}

func (d *ContextDialog) Close() {
	d.active = false
	d.overview = nil
	d.exts = nil
	d.vp.Reset()
	d.tab = 0
}

func (d *ContextDialog) body() []string {
	if d.tab == 1 {
		return d.exts
	}
	return d.overview
}

// wrappedBody folds the active tab's lines to the dialog width so a long
// line (e.g. an extension's injected text, which arrives as one logical
// line) wraps and stays scrollable instead of running off the right edge.
// Wrapping is width-aware and preserves the ANSI colour of each line.
func (d *ContextDialog) wrappedBody(width int) []string {
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
func (d *ContextDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return true
	case tui.KeyTab, tui.KeyRight:
		d.tab = (d.tab + 1) % contextTabCount
		// A new tab is unrelated content; holding the offset would open it
		// part-read. The viewport's geometry is refreshed by the next Fit.
		d.vp.Reset()
	case tui.KeyShiftTab, tui.KeyLeft:
		d.tab = (d.tab + contextTabCount - 1) % contextTabCount
		d.vp.Reset()
	default:
		// ↑/↓, PgUp/PgDn, Home/End — one implementation, shared with every other
		// scrolling dialog.
		d.vp.HandleKey(k)
	}
	return false
}

func (d *ContextDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("context"), width))
	lines = append(lines, d.tabBar(th))
	if d.sessionID != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("session %s", d.sessionID)))
	}
	if d.sessionPath != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+truncate(d.sessionPath, width-4)))
	}
	lines = append(lines, "")

	body := d.wrappedBody(width)
	d.vp.Fit(len(body), d.bodyRows())
	lines = append(lines, d.vp.Rows(th, body)...)
	lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("←→/tab switch · ↑/↓ scroll · home/end · esc")))
	lines = append(lines, FrameRule(th, width))
	return lines
}

func (d *ContextDialog) tabBar(th tui.Theme) string {
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
