package dialogs

import (
	"terva.sh/terva/packages/tui"
)

// SessionOpsDialog is the picker shown when the user runs `/session`
// without an argument. Offers the portable-file operations on the
// current conversation (export/import/fork/tree). A thin typed
// wrapper over the shared listPicker core.
type SessionOpsDialog struct {
	p listPicker
}

type SessionOpsItem struct {
	Label  string
	Action string // "export" | "import"
	Hint   string
}

type sessionOpsAction struct {
	Select bool
	Action string
	Close  bool
}

func NewSessionOpsDialog() *SessionOpsDialog { return &SessionOpsDialog{} }

// Open shows the picker. Items are usually both "export" and
// "import" but the caller can suppress either (e.g. hide export
// when the session is empty).
func (d *SessionOpsDialog) Open(items []SessionOpsItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		rows[i] = pickerItem{label: it.Label, hint: it.Hint, value: it.Action}
	}
	return d.p.open("session", "pick an action (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *SessionOpsDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *SessionOpsDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *SessionOpsDialog) HandleKey(k tui.Key) sessionOpsAction {
	act := d.p.handleKey(k)
	return sessionOpsAction{Select: act.Select, Action: act.Value, Close: act.Close}
}

// Render returns the dialog lines.
func (d *SessionOpsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
