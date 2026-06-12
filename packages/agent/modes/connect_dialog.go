package modes

import (
	"terva.sh/terva/packages/tui"
)

// connectDialog is the picker shown when the user runs `/connect`
// (alias `/telegram`) without an argument. Lists the three actions
// (connect, disconnect, status) and routes the choice back to the
// Interactive via connectAction.
//
// Shape mirrors logoutDialog: a tiny list of rows with the cursor
// moved by arrow keys and enter to pick, esc to cancel.
type connectDialog struct {
	active bool
	items  []connectItem
	cursor int
}

type connectItem struct {
	label  string
	action string // "connect" | "disconnect" | "status"
	hint   string // muted text shown after the label (e.g. "not configured", "active")
}

type connectAction struct {
	Select bool
	Action string
	Close  bool
}

func newConnectDialog() *connectDialog { return &connectDialog{} }

// Open shows the picker with items describing the current state.
// items is rendered in order; the caller builds it so connect is
// only offered when disconnected, and vice versa.
func (d *connectDialog) Open(items []connectItem) bool {
	if len(items) == 0 {
		return false
	}
	d.items = items
	d.cursor = 0
	d.active = true
	return true
}

// Close hides the dialog.
func (d *connectDialog) Close() { d.active = false }

// Active reports whether the dialog is consuming input.
func (d *connectDialog) Active() bool { return d != nil && d.active }

// HandleKey advances the selection or resolves the dialog.
func (d *connectDialog) HandleKey(k tui.Key) connectAction {
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
		return connectAction{Close: true}
	case tui.KeyEnter:
		if len(d.items) == 0 {
			d.Close()
			return connectAction{Close: true}
		}
		it := d.items[d.cursor]
		d.Close()
		return connectAction{Select: true, Action: it.action}
	}
	return connectAction{}
}

// Render returns the dialog lines.
func (d *connectDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, "telegram", width))
	lines = append(lines, th.FG256(th.Muted, "pick an action (\u2191/\u2193, enter, esc to cancel):"))
	for i, it := range d.items {
		plain := "  " + it.label
		if it.hint != "" {
			plain += "  " + th.FG256(th.Muted, "("+it.hint+")")
		}
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	lines = append(lines, frameRule(th, width))
	return lines
}
