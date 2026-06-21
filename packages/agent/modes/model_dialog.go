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

	// promoting is the transient "set as default" sub-prompt entered with
	// Ctrl+D: the next key picks the scope ([p]roject / [g]lobal) or cancels
	// (esc). The model to promote is captured when the prompt opens.
	promoting       bool
	promoteProvider string
	promoteModel    string
}

// modelDialogAction is returned by HandleKey.
type modelDialogAction struct {
	Select   bool
	Edit     bool   // open the config editor for Provider/Model
	Promote  bool   // also persist the pick as a default (Scope set)
	Scope    string // "project" | "global" when Promote
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
func (d *modelDialog) Close() { d.active = false; d.promoting = false }

// Active reports whether the dialog is visible and consumes input.
func (d *modelDialog) Active() bool { return d != nil && d.active }

// Render returns the dialog lines.
func (d *modelDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, "model", width))

	// Promote sub-prompt takes over the body: pick a scope or cancel.
	if d.promoting {
		lines = append(lines, th.FG256(th.Accent, fmt.Sprintf("  set %q as default:", d.promoteModel)))
		lines = append(lines, th.FG256(th.Muted, "    [p] project (this repo, when trusted)   [g] global (everywhere)   esc cancel"))
		lines = append(lines, frameRule(th, width))
		return lines
	}

	hint := d.p.hintLine("pick a model (↑/↓, enter, ctrl+e edit, ctrl+d set-default, esc) - type to filter, :img/:reasoning by capability")
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
	// Promote sub-prompt ("set as default"): the next key picks the scope or
	// cancels. handleNavKey is bypassed so p/g aren't swallowed as filter
	// input. Promote also switches to the model (Select), then persists it.
	if d.promoting {
		switch {
		case k.Kind == tui.KeyEsc:
			d.promoting = false
			return modelDialogAction{}
		case k.Kind == tui.KeyRune && (k.Rune == 'p' || k.Rune == 'P'):
			prov, model := d.promoteProvider, d.promoteModel
			d.Close()
			return modelDialogAction{Select: true, Promote: true, Scope: "project", Provider: prov, Model: model}
		case k.Kind == tui.KeyRune && (k.Rune == 'g' || k.Rune == 'G'):
			prov, model := d.promoteProvider, d.promoteModel
			d.Close()
			return modelDialogAction{Select: true, Promote: true, Scope: "global", Provider: prov, Model: model}
		}
		return modelDialogAction{} // ignore other keys while the prompt is up
	}
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
	case tui.KeyCtrlD:
		// Ctrl+D (not a bare "d", which the filter would swallow) opens the
		// "set as default" scope prompt for the highlighted model.
		m, ok := d.p.selected()
		if !ok {
			return modelDialogAction{}
		}
		d.promoting = true
		d.promoteProvider = m.Provider
		d.promoteModel = m.ID
		return modelDialogAction{}
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
