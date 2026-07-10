package dialogs

import (
	"terva.sh/terva/packages/tui"
)

// ConnectDialog is the picker shown when the user runs `/connect`
// (alias `/telegram`) without an argument. Lists the available
// actions — one "connect <name>" row per configured chat service
// (compiled-in connectors and connector extensions alike), plus
// disconnect/status — and routes the choice back to the Interactive
// via connectAction. A thin typed wrapper over the shared listPicker
// core.
type ConnectDialog struct {
	p listPicker
}

type ConnectItem struct {
	Label  string
	Action string // "connect <name>" | "disconnect" | "status"
	Hint   string // muted text shown after the label (e.g. "not configured", "active")
}

type connectAction struct {
	Select bool
	Action string
	Close  bool
}

func NewConnectDialog() *ConnectDialog { return &ConnectDialog{} }

// Open shows the picker with items describing the current state.
// items is rendered in order; the caller builds it so connect is
// only offered when disconnected, and vice versa.
func (d *ConnectDialog) Open(items []ConnectItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		rows[i] = pickerItem{label: it.Label, hint: it.Hint, value: it.Action}
	}
	return d.p.open("chat bridge", "pick an action (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *ConnectDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *ConnectDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *ConnectDialog) HandleKey(k tui.Key) connectAction {
	act := d.p.handleKey(k)
	return connectAction{Select: act.Select, Action: act.Value, Close: act.Close}
}

// Render returns the dialog lines.
func (d *ConnectDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
