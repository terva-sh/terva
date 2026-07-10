package dialogs

// Shared list-picker core (TUI plan Phase 1.2). The tiny action
// dialogs (logout, /session ops, /connect) were character-for-
// character clones of each other: an items slice, a cursor with
// hand-written bounds checks, identical Enter/Esc semantics, and the
// same header/hint/rows/rule rendering. listPicker owns all of that
// once; the dialogs keep only their typed Open/action wrappers.
//
// cursorWindow is the shared "keep the cursor visible in a capped
// window" math used by every scrolling picker (model, rescue, jump,
// settings) so the centering/clamping arithmetic can't drift apart
// per dialog again.

import (
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// pickerItem is one selectable row.
type pickerItem struct {
	label string // row text (rendered with a two-space indent)
	hint  string // optional muted "(hint)" suffix
	value string // opaque id handed back on selection
}

// pickerAction is the outcome of a key press.
type pickerAction struct {
	Select bool
	Value  string // value of the selected item when Select is set
	Close  bool
}

// listPicker is the shared core for small fixed list dialogs:
// up/down moves, enter selects (closing the picker), esc cancels.
type listPicker struct {
	active bool
	title  string // frameHeader title
	prompt string // muted hint line under the header
	items  []pickerItem
	cursor int
}

// open shows the picker. Returns false (and stays hidden) for an
// empty item list so callers fall back to a status message instead
// of an empty dialog.
func (p *listPicker) open(title, prompt string, items []pickerItem) bool {
	if len(items) == 0 {
		return false
	}
	p.title = title
	p.prompt = prompt
	p.items = items
	p.cursor = 0
	p.active = true
	return true
}

func (p *listPicker) close()         { p.active = false }
func (p *listPicker) isActive() bool { return p != nil && p.active }

// handleKey advances the selection or resolves the picker.
func (p *listPicker) handleKey(k tui.Key) pickerAction {
	switch k.Kind {
	case tui.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
	case tui.KeyDown:
		if p.cursor < len(p.items)-1 {
			p.cursor++
		}
	case tui.KeyEsc:
		p.close()
		return pickerAction{Close: true}
	case tui.KeyEnter:
		if len(p.items) == 0 {
			p.close()
			return pickerAction{Close: true}
		}
		it := p.items[p.cursor]
		p.close()
		return pickerAction{Select: true, Value: it.value}
	}
	return pickerAction{}
}

// render returns the picker lines: header, hint, rows (cursor row
// highlighted), closing rule.
func (p *listPicker) render(th tui.Theme, width int) []string {
	if !p.isActive() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, p.title, width))
	lines = append(lines, th.FG256(th.Muted, p.prompt))
	for i, it := range p.items {
		row := "  " + it.label
		if it.hint != "" {
			row += "  " + th.FG256(th.Muted, "("+it.hint+")")
		}
		if i == p.cursor {
			lines = append(lines, th.PadHighlight(row, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, row))
		}
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// CursorWindow returns the [start, end) slice bounds of a window of
// at most maxRows rows over total rows, centered on cursor and
// clamped to the list edges. With total <= maxRows the whole list is
// returned.
func CursorWindow(cursor, total, maxRows int) (start, end int) {
	if maxRows <= 0 || total <= maxRows {
		return 0, total
	}
	start = cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start+maxRows > total {
		start = total - maxRows
	}
	return start, start + maxRows
}

// WindowMoreAbove / windowMoreBelow are the standard muted "N more
// above/below" indicator rows around a windowed list render. Every
// scrolling dialog routes through these so the wording, styling, and
// translation live in exactly one place.
func WindowMoreAbove(th tui.Theme, start int) string {
	return th.FG256(th.Muted, "  "+i18n.T("↑ %d more above", start))
}

func WindowMoreBelow(th tui.Theme, total, end int) string {
	return th.FG256(th.Muted, "  "+i18n.T("↓ %d more below", total-end))
}
