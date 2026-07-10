package dialogs

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

type SettingsDialog struct {
	active       bool
	items        []SettingsItem
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

type SettingsItem struct {
	Key      string
	Label    string
	Desc     string
	Value    bool
	Options  []SettingsOption
	Choice   int
	Disabled bool
	Hint     string
}

type SettingsOption struct {
	Value string
	Label string
	Desc  string
}

type SettingsAction struct {
	Toggle      bool
	Key         string
	Value       bool
	StringValue string
	Close       bool
}

func NewSettingsDialog() *SettingsDialog { return &SettingsDialog{} }

func (d *SettingsDialog) Open(items []SettingsItem) bool {
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

func (d *SettingsDialog) Close() {
	d.active = false
	d.selecting = false
}
func (d *SettingsDialog) Active() bool { return d != nil && d.active }

func (d *SettingsDialog) HandleKey(k tui.Key) SettingsAction {
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
		return SettingsAction{Close: true}
	case tui.KeyEnter:
		return d.toggleCurrent()
	case tui.KeyRune:
		if k.Rune == ' ' {
			return d.toggleCurrent()
		}
	}
	return SettingsAction{}
}

func (d *SettingsDialog) handleOptionKey(k tui.Key) SettingsAction {
	it := d.items[d.cursor]
	switch k.Kind {
	case tui.KeyUp:
		if d.optionCursor > 0 {
			d.optionCursor--
		}
	case tui.KeyDown:
		if d.optionCursor < len(it.Options)-1 {
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
	return SettingsAction{}
}

func (d *SettingsDialog) toggleCurrent() SettingsAction {
	if len(d.items) == 0 {
		d.Close()
		return SettingsAction{Close: true}
	}
	it := d.items[d.cursor]
	if it.Disabled {
		return SettingsAction{}
	}
	if len(it.Options) > 0 {
		d.optionCursor = it.Choice
		if d.optionCursor < 0 || d.optionCursor >= len(it.Options) {
			d.optionCursor = 0
		}
		d.selecting = true
		return SettingsAction{}
	}
	it.Value = !it.Value
	d.items[d.cursor] = it
	return SettingsAction{Toggle: true, Key: it.Key, Value: it.Value}
}

func (d *SettingsDialog) selectCurrentOption() SettingsAction {
	if len(d.items) == 0 {
		d.Close()
		return SettingsAction{Close: true}
	}
	it := d.items[d.cursor]
	if len(it.Options) == 0 {
		d.selecting = false
		return SettingsAction{}
	}
	if d.optionCursor < 0 || d.optionCursor >= len(it.Options) {
		d.optionCursor = 0
	}
	it.Choice = d.optionCursor
	d.items[d.cursor] = it
	d.selecting = false
	return SettingsAction{Toggle: true, Key: it.Key, StringValue: it.Options[it.Choice].Value}
}

func (d *SettingsDialog) Render(th tui.Theme, width int) []string {
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
		if it.Value {
			box = "[✓]"
		}
		plain := "  " + box + " " + it.Label
		if len(it.Options) > 0 {
			box = "[→]"
			if it.Choice < 0 || it.Choice >= len(it.Options) {
				it.Choice = 0
			}
			plain = "  " + box + " " + it.Label + ": " + it.Options[it.Choice].Label
		}
		if it.Hint != "" {
			plain += "  " + th.FG256(th.Muted, "("+it.Hint+")")
		}
		if it.Disabled {
			body = append(body, th.FG256(th.Muted, plain))
		} else if i == d.cursor {
			body = append(body, th.PadHighlight(plain, width))
		} else {
			body = append(body, plain)
		}
		if it.Desc != "" {
			for _, desc := range wrapSettingDescription(it.Desc, width, 6) {
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
	lines = append(lines, FrameHeader(th, i18n.T("settings"), width))
	lines = append(lines, th.FG256(th.Muted, i18n.T("change with enter/space, esc to close:")))
	if d.scroll > 0 {
		lines = append(lines, WindowMoreAbove(th, d.scroll))
	}
	lines = append(lines, body[d.scroll:end]...)
	if end < len(body) {
		lines = append(lines, WindowMoreBelow(th, len(body), end))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

func (d *SettingsDialog) renderOptions(th tui.Theme, width int) []string {
	if len(d.items) == 0 || d.cursor < 0 || d.cursor >= len(d.items) {
		d.selecting = false
		return d.Render(th, width)
	}
	it := d.items[d.cursor]
	lines := []string{FrameHeader(th, i18n.T("settings: %s", it.Label), width)}
	if it.Desc != "" {
		lines = append(lines, th.FG256(th.Muted, it.Desc))
	}
	lines = append(lines, th.FG256(th.Muted, i18n.T("select with enter/space, esc to go back:")))
	for idx, opt := range it.Options {
		marker := "  "
		if idx == it.Choice {
			marker = "✓ "
		}
		plain := "  " + marker + opt.Label
		if idx == d.optionCursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, plain)
		}
		if opt.Desc != "" {
			for _, desc := range wrapSettingDescription(opt.Desc, width, 6) {
				lines = append(lines, th.FG256(th.Muted, desc))
			}
		}
	}
	lines = append(lines, FrameRule(th, width))
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
