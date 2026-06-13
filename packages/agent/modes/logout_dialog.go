package modes

import (
	"terva.sh/terva/packages/tui"
)

// logoutDialog is the picker shown when the user runs `/logout`
// without an argument. Lists each provider the user is currently
// logged into (apikey or oauth), plus an "all" entry and a cancel.
// A thin typed wrapper over the shared listPicker core.
type logoutDialog struct {
	p listPicker
}

// logoutItem is one row in the picker. target is what gets passed
// to doLogout ("anthropic", "openai", or "all"); method is the
// short tag shown in muted text next to the provider ("apikey" or
// "oauth"), empty for the "all" row.
type logoutItem struct {
	label  string
	target string
	method string
}

// logoutAction is the outcome of a key press.
type logoutAction struct {
	Select bool
	Target string // one of: "anthropic", "openai", "all"
	Close  bool
}

func newLogoutDialog() *logoutDialog { return &logoutDialog{} }

// Open populates the picker with whichever providers are currently
// logged in. Returns false if nothing to log out of; the caller
// should fall back to showing a status message instead of opening
// an empty dialog.
func (d *logoutDialog) Open(items []logoutItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		rows[i] = pickerItem{label: it.label, hint: it.method, value: it.target}
	}
	return d.p.open("logout", "choose what to log out of (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *logoutDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *logoutDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *logoutDialog) HandleKey(k tui.Key) logoutAction {
	act := d.p.handleKey(k)
	return logoutAction{Select: act.Select, Target: act.Value, Close: act.Close}
}

// Render returns the dialog lines.
func (d *logoutDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
