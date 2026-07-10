package dialogs

import (
	"terva.sh/terva/packages/tui"
)

// LogoutDialog is the picker shown when the user runs `/logout`
// without an argument. Lists each provider the user is currently
// logged into (apikey or oauth), plus an "all" entry and a cancel.
// A thin typed wrapper over the shared listPicker core.
type LogoutDialog struct {
	p listPicker
}

// LogoutItem is one row in the picker. target is what gets passed
// to doLogout ("anthropic", "openai", or "all"); method is the
// short tag shown in muted text next to the provider ("apikey" or
// "oauth"), empty for the "all" row.
type LogoutItem struct {
	Label  string
	Target string
	Method string
}

// logoutAction is the outcome of a key press.
type logoutAction struct {
	Select bool
	Target string // one of: "anthropic", "openai", "all"
	Close  bool
}

func NewLogoutDialog() *LogoutDialog { return &LogoutDialog{} }

// Open populates the picker with whichever providers are currently
// logged in. Returns false if nothing to log out of; the caller
// should fall back to showing a status message instead of opening
// an empty dialog.
func (d *LogoutDialog) Open(items []LogoutItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		rows[i] = pickerItem{label: it.Label, hint: it.Method, value: it.Target}
	}
	return d.p.open("logout", "choose what to log out of (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *LogoutDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *LogoutDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *LogoutDialog) HandleKey(k tui.Key) logoutAction {
	act := d.p.handleKey(k)
	return logoutAction{Select: act.Select, Target: act.Value, Close: act.Close}
}

// Render returns the dialog lines.
func (d *LogoutDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
