package dialogs

import (
	"strings"

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
	// Endpoint marks a row that REMOVES a named openai-compatible endpoint
	// instead of clearing a credential. Two different verbs with two different
	// consequences: a logout forgets a secret and leaves the provider standing to
	// sign back into, while this forgets which machine, which port and which
	// context window — and nothing in terva remembers those but that one entry.
	Endpoint bool
}

// logoutAction is the outcome of a key press.
type logoutAction struct {
	Select bool
	Target string // a provider id, an endpoint id, or "all"
	// Endpoint distinguishes the two verbs the picker can resolve to. It rides
	// the row rather than being re-derived from the target, because "is this id
	// an endpoint" is a question the caller would have to ask config again — and
	// answer differently if the operator removed it in between.
	Endpoint bool
	Close    bool
}

// endpointTargetPrefix tags an endpoint row inside the picker's opaque value, so
// the removal verb survives the trip through listPicker (which carries one
// string per row and knows nothing about providers).
const endpointTargetPrefix = "endpoint:"

func NewLogoutDialog() *LogoutDialog { return &LogoutDialog{} }

// Open populates the picker with whichever providers are currently
// logged in. Returns false if nothing to log out of; the caller
// should fall back to showing a status message instead of opening
// an empty dialog.
func (d *LogoutDialog) Open(items []LogoutItem) bool {
	rows := make([]pickerItem, len(items))
	for i, it := range items {
		value := it.Target
		if it.Endpoint {
			value = endpointTargetPrefix + it.Target
		}
		rows[i] = pickerItem{label: it.Label, hint: it.Method, value: value}
	}
	return d.p.open("logout", "choose what to log out of or remove (↑/↓, enter, esc to cancel):", rows)
}

// Close hides the dialog.
func (d *LogoutDialog) Close() { d.p.close() }

// Active reports whether the dialog is consuming input.
func (d *LogoutDialog) Active() bool { return d != nil && d.p.isActive() }

// HandleKey advances the selection or resolves the dialog.
func (d *LogoutDialog) HandleKey(k tui.Key) logoutAction {
	act := d.p.handleKey(k)
	target, endpoint := strings.CutPrefix(act.Value, endpointTargetPrefix)
	return logoutAction{Select: act.Select, Target: target, Endpoint: endpoint, Close: act.Close}
}

// Render returns the dialog lines.
func (d *LogoutDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	return d.p.render(th, width)
}
