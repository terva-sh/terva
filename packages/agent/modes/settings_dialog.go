package modes

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/tui"
)

type settingsDialog struct {
	active       bool
	items        []settingsItem
	cursor       int
	selecting    bool
	optionCursor int

	// MaxRows caps the rendered body (item rows + wrapped
	// descriptions); the window scrolls to keep the cursor item
	// visible. Set by the overlay registry from the live terminal
	// height — without the cap the full settings list is taller than
	// a 24-row terminal and the header clips off the top. 0 = no cap.
	MaxRows int
	scroll  int
}

type settingsItem struct {
	key      string
	label    string
	desc     string
	value    bool
	options  []settingsOption
	choice   int
	disabled bool
	hint     string
}

type settingsOption struct {
	value string
	label string
	desc  string
}

type settingsAction struct {
	Toggle      bool
	Key         string
	Value       bool
	StringValue string
	Close       bool
}

func newSettingsDialog() *settingsDialog { return &settingsDialog{} }

func (d *settingsDialog) Open(items []settingsItem) bool {
	if len(items) == 0 {
		return false
	}
	d.items = items
	d.cursor = 0
	d.selecting = false
	d.optionCursor = 0
	d.scroll = 0
	d.active = true
	return true
}

func (d *settingsDialog) Close() {
	d.active = false
	d.selecting = false
}
func (d *settingsDialog) Active() bool { return d != nil && d.active }

func (d *settingsDialog) HandleKey(k tui.Key) settingsAction {
	if d.selecting {
		return d.handleOptionKey(k)
	}
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.items)-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		d.Close()
		return settingsAction{Close: true}
	case tui.KeyEnter:
		return d.toggleCurrent()
	case tui.KeyRune:
		if k.Rune == ' ' {
			return d.toggleCurrent()
		}
	}
	return settingsAction{}
}

func (d *settingsDialog) handleOptionKey(k tui.Key) settingsAction {
	it := d.items[d.cursor]
	switch k.Kind {
	case tui.KeyUp:
		if d.optionCursor > 0 {
			d.optionCursor--
		}
	case tui.KeyDown:
		if d.optionCursor < len(it.options)-1 {
			d.optionCursor++
		}
	case tui.KeyEsc:
		d.selecting = false
	case tui.KeyEnter:
		return d.selectCurrentOption()
	case tui.KeyRune:
		if k.Rune == ' ' {
			return d.selectCurrentOption()
		}
	}
	return settingsAction{}
}

func (d *settingsDialog) toggleCurrent() settingsAction {
	if len(d.items) == 0 {
		d.Close()
		return settingsAction{Close: true}
	}
	it := d.items[d.cursor]
	if it.disabled {
		return settingsAction{}
	}
	if len(it.options) > 0 {
		d.optionCursor = it.choice
		if d.optionCursor < 0 || d.optionCursor >= len(it.options) {
			d.optionCursor = 0
		}
		d.selecting = true
		return settingsAction{}
	}
	it.value = !it.value
	d.items[d.cursor] = it
	return settingsAction{Toggle: true, Key: it.key, Value: it.value}
}

func (d *settingsDialog) selectCurrentOption() settingsAction {
	if len(d.items) == 0 {
		d.Close()
		return settingsAction{Close: true}
	}
	it := d.items[d.cursor]
	if len(it.options) == 0 {
		d.selecting = false
		return settingsAction{}
	}
	if d.optionCursor < 0 || d.optionCursor >= len(it.options) {
		d.optionCursor = 0
	}
	it.choice = d.optionCursor
	d.items[d.cursor] = it
	d.selecting = false
	return settingsAction{Toggle: true, Key: it.key, StringValue: it.options[it.choice].value}
}

func (d *settingsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	if d.selecting {
		return d.renderOptions(th, width)
	}
	// Build the body (item rows interleaved with wrapped description
	// rows), tracking which body line the cursor item starts on so
	// the scroll window can follow it.
	var body []string
	cursorLine := 0
	for i, it := range d.items {
		if i == d.cursor {
			cursorLine = len(body)
		}
		box := "[ ]"
		if it.value {
			box = "[✓]"
		}
		plain := "  " + box + " " + it.label
		if len(it.options) > 0 {
			box = "[→]"
			if it.choice < 0 || it.choice >= len(it.options) {
				it.choice = 0
			}
			plain = "  " + box + " " + it.label + ": " + it.options[it.choice].label
		}
		if it.hint != "" {
			plain += "  " + th.FG256(th.Muted, "("+it.hint+")")
		}
		if it.disabled {
			body = append(body, th.FG256(th.Muted, plain))
		} else if i == d.cursor {
			body = append(body, th.PadHighlight(plain, width))
		} else {
			body = append(body, plain)
		}
		if it.desc != "" {
			for _, desc := range wrapSettingDescription(it.desc, width, 6) {
				body = append(body, th.FG256(th.Muted, desc))
			}
		}
	}

	// Window the body so the dialog fits short terminals, keeping the
	// cursor item's first row in view.
	maxRows := d.MaxRows
	if maxRows <= 0 || maxRows > len(body) {
		maxRows = len(body)
	}
	if cursorLine < d.scroll {
		d.scroll = cursorLine
	}
	if cursorLine >= d.scroll+maxRows {
		d.scroll = cursorLine - maxRows + 1
	}
	if d.scroll > len(body)-maxRows {
		d.scroll = len(body) - maxRows
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
	end := d.scroll + maxRows

	var lines []string
	lines = append(lines, frameHeader(th, "settings", width))
	lines = append(lines, th.FG256(th.Muted, "change with enter/space, esc to close:"))
	if d.scroll > 0 {
		lines = append(lines, windowMoreAbove(th, d.scroll))
	}
	lines = append(lines, body[d.scroll:end]...)
	if end < len(body) {
		lines = append(lines, windowMoreBelow(th, len(body), end))
	}
	lines = append(lines, frameRule(th, width))
	return lines
}

func (d *settingsDialog) renderOptions(th tui.Theme, width int) []string {
	if len(d.items) == 0 || d.cursor < 0 || d.cursor >= len(d.items) {
		d.selecting = false
		return d.Render(th, width)
	}
	it := d.items[d.cursor]
	lines := []string{frameHeader(th, "settings: "+it.label, width)}
	if it.desc != "" {
		lines = append(lines, th.FG256(th.Muted, it.desc))
	}
	lines = append(lines, th.FG256(th.Muted, "select with enter/space, esc to go back:"))
	for idx, opt := range it.options {
		marker := "  "
		if idx == it.choice {
			marker = "✓ "
		}
		plain := "  " + marker + opt.label
		if idx == d.optionCursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, plain)
		}
		if opt.desc != "" {
			for _, desc := range wrapSettingDescription(opt.desc, width, 6) {
				lines = append(lines, th.FG256(th.Muted, desc))
			}
		}
	}
	lines = append(lines, frameRule(th, width))
	return lines
}

func wrapSettingDescription(desc string, width, indent int) []string {
	prefix := strings.Repeat(" ", indent)
	limit := width - indent
	if limit < 20 {
		limit = 20
	}
	words := strings.Fields(desc)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		candidate := line + " " + word
		if runewidth.StringWidth(candidate) <= limit {
			line = candidate
			continue
		}
		lines = append(lines, prefix+line)
		line = word
	}
	lines = append(lines, prefix+line)
	return lines
}
