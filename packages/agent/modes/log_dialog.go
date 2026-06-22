package modes

import "terva.sh/terva/packages/tui"

// logDialog is a read-only scrollable viewer for an extension's or MCP
// server's log, opened with 'l' from the /extensions and /mcp dialogs so
// the user can read why something is off without leaving the TUI. The host
// reads the file tail and passes the lines; this just scrolls them.
type logDialog struct {
	active bool
	title  string
	lines  []string
	top    int // index of the first visible line
}

const logViewRows = 16

func newLogDialog() *logDialog { return &logDialog{} }

// Open shows lines under title, scrolled to the bottom (the newest output,
// where an error usually is).
func (d *logDialog) Open(title string, lines []string) {
	if len(lines) == 0 {
		lines = []string{"(log is empty)"}
	}
	d.active = true
	d.title = title
	d.lines = lines
	d.top = d.maxTop()
}

func (d *logDialog) Active() bool { return d != nil && d.active }
func (d *logDialog) Close()       { d.active = false }

func (d *logDialog) maxTop() int {
	if len(d.lines) <= logViewRows {
		return 0
	}
	return len(d.lines) - logViewRows
}

func (d *logDialog) clampTop() {
	if d.top < 0 {
		d.top = 0
	}
	if m := d.maxTop(); d.top > m {
		d.top = m
	}
}

// HandleKey scrolls the view and returns true when it closed.
func (d *logDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}
	switch k.Kind {
	case tui.KeyUp:
		d.top--
	case tui.KeyDown:
		d.top++
	case tui.KeyPageUp:
		d.top -= logViewRows
	case tui.KeyPageDown:
		d.top += logViewRows
	case tui.KeyHome:
		d.top = 0
	case tui.KeyEnd:
		d.top = d.maxTop()
	case tui.KeyEsc:
		d.Close()
		return true
	case tui.KeyRune:
		switch k.Rune {
		case 'g':
			d.top = 0
		case 'G':
			d.top = d.maxTop()
		case 'q':
			d.Close()
			return true
		}
	}
	d.clampTop()
	return false
}

// Render returns the dialog lines.
func (d *logDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	lines := []string{
		frameHeader(th, d.title, width),
		th.FG256(th.Muted, "↑/↓ · PgUp/PgDn · g/G top/bottom · esc"),
	}
	end := d.top + logViewRows
	if end > len(d.lines) {
		end = len(d.lines)
	}
	if d.top > 0 {
		lines = append(lines, windowMoreAbove(th, d.top))
	}
	for i := d.top; i < end; i++ {
		lines = append(lines, th.FG256(th.Muted, "  "+truncate(d.lines[i], width-4)))
	}
	if end < len(d.lines) {
		lines = append(lines, windowMoreBelow(th, len(d.lines), end))
	}
	lines = append(lines, frameRule(th, width))
	return lines
}
