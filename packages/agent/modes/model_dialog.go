package modes

import (
	"fmt"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// modelDialog is an inline picker for choosing the active model.
// It lists all models known to the provider package (baked-in catalog
// + any live entries discovered via /v1/models) sorted by provider
// then model id, and lets the user pick one with arrow keys + enter.
// Typing characters narrows the list via a fuzzy substring match that
// ignores punctuation (e.g. "opus46" matches "claude-opus-4-6").
// Filtering, scrolling, and row rendering live in the shared
// modelPicker core.
type modelDialog struct {
	active bool
	p      modelPicker
}

// modelDialogAction is returned by HandleKey.
type modelDialogAction struct {
	Select   bool
	Edit     bool // open the config editor for Provider/Model
	Provider string
	Model    string
	Close    bool
}

func newModelDialog() *modelDialog {
	return &modelDialog{}
}

// Open shows the dialog. current is the currently active model id so
// it can be pre-selected.
func (d *modelDialog) Open(current string, loggedInProviders []string) {
	d.active = true
	// Only surface models the user can actually reach: a provider is
	// shown only when it has a resolvable credential (api key, oauth
	// subscription, or the always-available ollama). An empty
	// loggedInProviders list therefore yields an empty picker rather
	// than dumping the entire ~900-model catalog.
	provSet := map[string]bool{}
	for _, p := range loggedInProviders {
		provSet[p] = true
	}
	var filtered []provider.Model
	for _, m := range provider.Active() {
		if provSet[m.Provider] {
			filtered = append(filtered, m)
		}
	}
	d.p.setCatalog(filtered, current, 14)
}

// Close hides the dialog.
func (d *modelDialog) Close() { d.active = false }

// Active reports whether the dialog is visible and consumes input.
func (d *modelDialog) Active() bool { return d != nil && d.active }

// Render returns the dialog lines.
func (d *modelDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, "model", width))

	hint := d.p.hintLine("pick a model (↑/↓, enter, ctrl+e edit, esc) - type to filter, :img/:reasoning by capability")
	lines = append(lines, th.FG256(th.Muted, hint))

	if len(d.p.view) == 0 {
		msg := "  no models match " + fmt.Sprintf("%q", d.p.query)
		if len(d.p.all) == 0 {
			msg = "  no credentials found - run /login to add an api key or subscription"
		}
		lines = append(lines, th.FG256(th.Muted, msg))
		lines = append(lines, frameRule(th, width))
		return lines
	}

	lines = append(lines, d.p.renderRows(th, width)...)
	lines = append(lines, frameRule(th, width))
	return lines
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *modelDialog) HandleKey(k tui.Key) modelDialogAction {
	if d.p.handleNavKey(k) {
		return modelDialogAction{}
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return modelDialogAction{Close: true}
	case tui.KeyEnter:
		m, ok := d.p.selected()
		d.Close()
		if !ok {
			return modelDialogAction{Close: true}
		}
		return modelDialogAction{Select: true, Provider: m.Provider, Model: m.ID}
	case tui.KeyCtrlE:
		// Edit the selected model's config. Ctrl+E (not a bare "e",
		// which the type-to-filter input would swallow) opens the
		// editor; the picker closes so the edit overlay takes over.
		m, ok := d.p.selected()
		if !ok {
			return modelDialogAction{}
		}
		d.Close()
		return modelDialogAction{Edit: true, Provider: m.Provider, Model: m.ID}
	}
	return modelDialogAction{}
}
