package modes

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/tui"
)

type extPanelDialog struct {
	active bool
	ext    string
	id     string
	title  string
	lines  []string
	footer string
}

func newExtPanelDialog() *extPanelDialog { return &extPanelDialog{} }

func (d *extPanelDialog) Active() bool { return d != nil && d.active }

func (d *extPanelDialog) Open(ext, id, title string, lines []string, footer string) {
	d.active = true
	d.ext = ext
	d.id = id
	d.title = title
	d.lines = append([]string(nil), lines...)
	d.footer = footer
}

func (d *extPanelDialog) Update(title string, lines []string, footer string) {
	if !d.active {
		return
	}
	if title != "" {
		d.title = title
	}
	d.lines = append(d.lines[:0], lines...)
	d.footer = footer
}

func (d *extPanelDialog) Close() {
	d.active = false
	d.ext = ""
	d.id = ""
	d.title = ""
	d.lines = nil
	d.footer = ""
}

func (d *extPanelDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	title := d.title
	if title == "" {
		title = d.ext
	}
	out := []string{frameHeaderColor(th, title, width, th.Accent)}
	for _, l := range d.lines {
		plain := stripANSIBytes(l)
		trimmed := strings.TrimLeft(plain, " ")
		// Selection markers, in order of precedence:
		//   "▸ " / "● "  visible glyph the user sees
		//   "\u200b"      invisible zero-width-space sentinel for
		//                 extensions that want the row highlight
		//                 without rendering an arrow
		selected := strings.HasPrefix(trimmed, "▸ ") ||
			strings.HasPrefix(trimmed, "● ") ||
			strings.HasPrefix(trimmed, "\u200b")
		out = append(out, styleExtPanelLine(th, l, plain, width, selected))
	}
	if strings.TrimSpace(d.footer) != "" {
		out = append(out, "")
		out = append(out, th.FG256(th.Muted, d.footer))
	}
	out = append(out, frameRuleColor(th, width, th.Accent))
	return out
}

func styleExtPanelLine(th tui.Theme, raw string, plain string, width int, selected bool) string {
	if selected {
		if visible := runewidth.StringWidth(plain); visible < width {
			plain += strings.Repeat(" ", width-visible)
		}
		base := th.SelectionStyle()
		green := th.SelectionStyleFG(th.Tool)
		return base + strings.ReplaceAll(plain, "✓", green+"✓"+base) + "\x1b[0m"
	}
	if raw != plain {
		return raw + "\x1b[0m"
	}
	styled := th.FG256(th.Muted, plain)
	styled = strings.ReplaceAll(styled, "✓", th.FG256(th.Tool, "✓"))
	return styled
}
