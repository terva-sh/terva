package modes

import (
	"terva.sh/terva/packages/tui"
)

// sessionOpsDialog is the picker shown when the user runs `/session`
// without an argument. Offers the portable-file operations on the
// current conversation (export/import/fork/tree). A thin typed
// wrapper over the shared listPicker core.
type sessionOpsDialog struct {
	p listPicker
}

type sessionOpsItem struct {
	label  string
	action string // "export" | "import"
	hint   string
}

type sessionOpsAction struct {
	Select bool
	Action string
	Close  bool
}

func newSessionOpsDialog() *sessionOpsDialog { return &sessionOpsDialog{} }

// Open shows the picker. Items are usually both "export" and
// "import" but the caller can suppress either (e.g. hide export
// when the session is empty).
func (d *sessionOpsDialog) Open(items []sessionOpsItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		rows[i] = pickerItem{label: it.label, hint: it.hint, value: it.action}
	}
	return d.p.open("session", "pick an action (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *sessionOpsDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *sessionOpsDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *sessionOpsDialog) HandleKey(k tui.Key) sessionOpsAction {
	act := d.p.handleKey(k)
	return sessionOpsAction{Select: act.Select, Action: act.Value, Close: act.Close}
}

// Render returns the dialog lines.
func (d *sessionOpsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
