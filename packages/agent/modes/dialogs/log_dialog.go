package dialogs

import "terva.sh/terva/packages/tui"

// LogDialog is a read-only scrollable viewer for an extension's or MCP
// server's log, opened with 'l' from the /extensions and /mcp dialogs so
// the user can read why something is off without leaving the TUI. The host
// reads the file tail and passes the lines; this just scrolls them.
type LogDialog struct {
	active bool
	title  string
	lines  []string
	top    int // index of the first visible line
}

const logViewRows = 16

func NewLogDialog() *LogDialog { return &LogDialog{} }

// Open shows lines under title, scrolled to the bottom (the newest output,
// where an error usually is).
func (d *LogDialog) Open(title string, lines []string) {
	if len(lines) == 0 {
		lines = []string{"(log is empty)"}
	}
	d.active = true
	d.title = title
	d.lines = lines
	d.top = d.maxTop()
}

func (d *LogDialog) Active() bool { return d != nil && d.active }
func (d *LogDialog) Close()       { d.active = false }

func (d *LogDialog) maxTop() int {
	if len(d.lines) <= logViewRows {
		return 0
	}
	return len(d.lines) - logViewRows
}

func (d *LogDialog) clampTop() {
	if d.top < 0 {
		d.top = 0
	}
	if m := d.maxTop(); d.top > m {
		d.top = m
	}
}

// HandleKey scrolls the view and returns true when it closed.
func (d *LogDialog) HandleKey(k tui.Key) (closed bool) {
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
func (d *LogDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	lines := []string{
		FrameHeader(th, d.title, width),
		th.FG256(th.Muted, "↑/↓ · PgUp/PgDn · g/G top/bottom · esc"),
	}
	end := d.top + logViewRows
	if end > len(d.lines) {
		end = len(d.lines)
	}
	if d.top > 0 {
		lines = append(lines, WindowMoreAbove(th, d.top))
	}
	for i := d.top; i < end; i++ {
		lines = append(lines, th.FG256(th.Muted, "  "+truncate(d.lines[i], width-4)))
	}
	if end < len(d.lines) {
		lines = append(lines, WindowMoreBelow(th, len(d.lines), end))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}
