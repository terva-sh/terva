package dialogs

import (
	"fmt"
	"sort"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// modelDialog is a two-level picker for choosing the active model. Stage 1 is a
// short provider list (the user's logged-in providers, plus a synthetic ★
// favorites view that spans providers); stage 2 is the scoped model list, which
// rides the shared modelPicker core (fuzzy filter, :img/:reasoning capability
// tokens, scroll). Splitting by provider keeps a huge catalogue — an OpenRouter
// or LiteLLM endpoint with hundreds of models — navigable, and favorites pin the
// handful you actually use to the top.
//
// stageModel is the iota zero value on purpose: a dialog built without Open()
// (tests that drive the picker core directly) defaults to the model stage.
const (
	stageModel = iota
	stageProvider
)

// providerRow is one entry in the stage-1 provider list.
type providerRow struct {
	name   string // provider name; "" for the synthetic favorites entry
	label  string // display text
	count  int    // VISIBLE models in scope
	hidden int    // models in scope the visibility rules keep out of the list
	fav    bool   // the cross-provider ★ favorites entry
}

type ModelDialog struct {
	active  bool
	stage   int
	current string // active model id, threaded into the scoped picker

	p modelPicker // stage-2 model list

	// stage-1 provider list state.
	providers  []providerRow
	provCursor int
	provQuery  string

	provSet    map[string]bool  // logged-in providers, for re-reading Active()
	allModels  []provider.Model // full logged-in catalogue, scoped per provider
	favorites  map[string]bool  // "provider/id" -> favorited
	hidden     map[string]bool  // "provider/id" -> hidden from the list
	scope      providerRow      // the current stage-2 scope (for re-scoping)
	scopeLabel string           // stage-2 breadcrumb ("openrouter" / "★ favorites")
	single     bool             // opened straight into one provider -> esc closes
	catalogRev uint64           // provider.CatalogRevision() at last read

	// promote sub-prompt (Ctrl+D "set as default").
	promoting       bool
	promoteProvider string
	promoteModel    string
}

// modelDialogAction is returned by HandleKey for the overlay host to apply.
type modelDialogAction struct {
	Select   bool
	Edit     bool   // open the config editor for Provider/Model
	Promote  bool   // also persist the pick as a default (Scope set)
	Scope    string // "project" | "global" when Promote
	Favorite bool   // toggle the favorite flag for Provider/Model
	FavOn    bool   // the new favorite state when Favorite
	Hide     bool   // toggle the hidden flag for Provider/Model
	HideOn   bool   // the new hidden state when Hide
	Provider string
	Model    string
	Close    bool
}

func NewModelDialog() *ModelDialog { return &ModelDialog{} }

// Open shows the dialog. current is the active model id (for the ● marker);
// loggedInProviders gates which providers/models are reachable; favorites are
// the "provider/id" keys to pin and star.
func (d *ModelDialog) Open(current string, loggedInProviders, favorites, hidden []string) {
	d.active = true
	d.promoting = false
	d.provQuery = ""
	d.provCursor = 0
	d.current = current
	d.catalogRev = provider.CatalogRevision()

	d.favorites = make(map[string]bool, len(favorites))
	for _, k := range favorites {
		d.favorites[k] = true
	}
	d.p.favorites = d.favorites // shared map: toggles in the picker reflect here

	d.hidden = make(map[string]bool, len(hidden))
	for _, k := range hidden {
		d.hidden[k] = true
	}
	d.p.hidden = d.hidden // shared map, same as favorites above

	d.provSet = map[string]bool{}
	for _, p := range loggedInProviders {
		d.provSet[p] = true
	}
	d.reloadModels()
	d.rebuildProviderList()

	// Single provider and nothing favorited: skip the provider step entirely.
	if len(d.providers) == 1 && !d.providers[0].fav {
		d.single = true
		d.enterProvider(d.providers[0])
		return
	}
	d.single = false
	d.stage = stageProvider
}

// reloadModels re-reads the live catalog (Active() can grow as background
// /v1/models discovery completes) filtered to the logged-in providers.
func (d *ModelDialog) reloadModels() {
	d.allModels = nil
	for _, m := range provider.Active() {
		if d.provSet[m.Provider] {
			d.allModels = append(d.allModels, m)
		}
	}
}

// rebuildProviderList recomputes the stage-1 list (counts + the synthetic ★
// favorites entry) from allModels and the current favorites. Called on Open,
// after a favorite toggle, and on a catalog refresh.
func (d *ModelDialog) rebuildProviderList() {
	counts := map[string]int{}
	hiddenCounts := map[string]int{}
	favCount := 0
	for _, m := range d.allModels {
		if d.hidden[modelKey(m)] {
			// Counted separately, not skipped: the row still has to appear (see
			// below), and the number is what tells the user there is something
			// behind ":hidden" worth looking at.
			hiddenCounts[m.Provider]++
			continue
		}
		counts[m.Provider]++
		if d.favorites[modelKey(m)] {
			favCount++
		}
	}
	d.providers = nil
	if favCount > 0 {
		d.providers = append(d.providers, providerRow{label: i18n.T("★ favorites"), count: favCount, fav: true})
	}
	// Union of both maps, so a provider whose models are ALL hidden keeps its
	// row. Dropping it would be a trap with no way out: the only route to
	// ":hidden" is through a provider's model list, so hiding the last model of
	// a provider would make every model it has unreachable — unhideable except
	// by hand-editing config.json.
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	for n := range hiddenCounts {
		if counts[n] == 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		d.providers = append(d.providers, providerRow{
			name: n, label: n, count: counts[n], hidden: hiddenCounts[n],
		})
	}
}

// scopeModelsFor returns the models in a provider row's scope (a real
// provider's models, or every favorited model for the ★ favorites row).
func (d *ModelDialog) scopeModelsFor(row providerRow) []provider.Model {
	var models []provider.Model
	for _, m := range d.allModels {
		if (row.fav && d.favorites[modelKey(m)]) || (!row.fav && m.Provider == row.name) {
			models = append(models, m)
		}
	}
	return models
}

// enterProvider scopes the model list to row and switches to stage 2.
func (d *ModelDialog) enterProvider(row providerRow) {
	d.scope = row
	d.scopeLabel = row.label
	d.p.favorites = d.favorites
	d.p.setCatalog(d.scopeModelsFor(row), d.current, 14)
	d.stage = stageModel
}

// reloadCatalog re-reads a grown catalog and rebuilds the provider list; in the
// model stage it re-scopes while preserving the live filter and the
// highlighted model, so late-arriving discovery doesn't yank the user around.
func (d *ModelDialog) reloadCatalog() {
	d.reloadModels()
	d.rebuildProviderList()
	if d.provCursor >= len(d.providers) && len(d.providers) > 0 {
		d.provCursor = len(d.providers) - 1
	}
	if d.stage == stageModel {
		// reload preserves the live filter and the highlighted model.
		d.p.reload(d.scopeModelsFor(d.scope))
	}
}

// filteredProviders applies the stage-1 type-to-filter to the provider list.
func (d *ModelDialog) filteredProviders() []providerRow {
	if d.provQuery == "" {
		return d.providers
	}
	q := strings.ToLower(d.provQuery)
	var out []providerRow
	for _, r := range d.providers {
		if strings.Contains(strings.ToLower(r.label), q) {
			out = append(out, r)
		}
	}
	return out
}

// Close hides the dialog.
func (d *ModelDialog) Close() { d.active = false; d.promoting = false }

// Active reports whether the dialog is visible and consumes input.
func (d *ModelDialog) Active() bool { return d != nil && d.active }

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *ModelDialog) HandleKey(k tui.Key) modelDialogAction {
	if d.promoting {
		return d.handlePromoteKey(k)
	}
	if d.stage == stageProvider {
		return d.handleProviderKey(k)
	}
	return d.handleModelKey(k)
}

// handlePromoteKey runs the Ctrl+D "set as default" scope sub-prompt: the next
// key picks the scope or cancels. handleNavKey is bypassed so p/g aren't
// swallowed as filter input. Promote also switches to the model, then persists.
func (d *ModelDialog) handlePromoteKey(k tui.Key) modelDialogAction {
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

func (d *ModelDialog) handleProviderKey(k tui.Key) modelDialogAction {
	rows := d.filteredProviders()
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return modelDialogAction{Close: true}
	case tui.KeyUp:
		if d.provCursor > 0 {
			d.provCursor--
		}
	case tui.KeyDown:
		if d.provCursor < len(rows)-1 {
			d.provCursor++
		}
	case tui.KeyEnter:
		if d.provCursor >= 0 && d.provCursor < len(rows) {
			d.enterProvider(rows[d.provCursor])
		}
	case tui.KeyBackspace:
		if r := []rune(d.provQuery); len(r) > 0 {
			d.provQuery = string(r[:len(r)-1])
			d.provCursor = 0
		}
	case tui.KeyRune:
		if !k.Alt && !k.Ctrl && k.Rune >= 0x20 && k.Rune < 0x7f {
			d.provQuery += string(k.Rune)
			d.provCursor = 0
		}
	}
	return modelDialogAction{}
}

func (d *ModelDialog) handleModelKey(k tui.Key) modelDialogAction {
	if d.p.handleNavKey(k) {
		return modelDialogAction{}
	}
	switch k.Kind {
	case tui.KeyEsc:
		if d.single {
			d.Close()
			return modelDialogAction{Close: true}
		}
		d.stage = stageProvider // back to the provider list
		return modelDialogAction{}
	case tui.KeyEnter:
		m, ok := d.p.selected()
		d.Close()
		if !ok {
			return modelDialogAction{Close: true}
		}
		return modelDialogAction{Select: true, Provider: m.Provider, Model: m.ID}
	case tui.KeyCtrlF:
		// Toggle favorite for the highlighted model (Ctrl+F, not a bare "f"
		// which the type-to-filter would swallow). The picker re-sorts and
		// keeps the cursor on the model; the host persists the change.
		m, on, ok := d.p.toggleFavorite()
		if !ok {
			return modelDialogAction{}
		}
		d.rebuildProviderList() // reflect the new ★ favorites entry/count when we back out
		return modelDialogAction{Favorite: true, FavOn: on, Provider: m.Provider, Model: m.ID}
	case tui.KeyCtrlK:
		// Toggle hidden for the highlighted model. The picker keeps the cursor
		// in place rather than re-sorting, so a restored model does not jump
		// out from under the user mid-list; the host persists the change.
		//
		// Ctrl+K, not the obvious Ctrl+H, and this is not a preference: a
		// terminal sends Ctrl+H as 0x08, which input.go maps to KeyBackspace.
		// The two are indistinguishable here, so binding "hide" to Ctrl+H would
		// fire it on every backspace in the type-to-filter.
		m, on, ok := d.p.toggleHidden()
		if !ok {
			return modelDialogAction{}
		}
		// The row must leave (or join) the list it is currently in, and the
		// provider counts have to follow it.
		d.p.reload(d.scopeModelsFor(d.scope))
		d.rebuildProviderList()
		return modelDialogAction{Hide: true, HideOn: on, Provider: m.Provider, Model: m.ID}
	case tui.KeyCtrlD:
		m, ok := d.p.selected()
		if !ok {
			return modelDialogAction{}
		}
		d.promoting = true
		d.promoteProvider = m.Provider
		d.promoteModel = m.ID
		return modelDialogAction{}
	case tui.KeyCtrlE:
		m, ok := d.p.selected()
		if !ok {
			return modelDialogAction{}
		}
		d.Close()
		return modelDialogAction{Edit: true, Provider: m.Provider, Model: m.ID}
	}
	return modelDialogAction{}
}

// Render returns the dialog lines for the active stage.
func (d *ModelDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	// Pick up models that arrived via background /v1/models discovery after
	// the picker opened (e.g. logging into OpenRouter, then opening /model
	// before its ~340 models finished loading).
	if r := provider.CatalogRevision(); r != d.catalogRev {
		d.catalogRev = r
		d.reloadCatalog()
	}
	if d.promoting {
		lines := []string{FrameHeader(th, i18n.T("model"), width)}
		lines = append(lines, th.FG256(th.Accent, "  "+i18n.T("set %q as default:", d.promoteModel)))
		lines = append(lines, th.FG256(th.Muted, "    "+i18n.T("[p] project (this repo, when trusted)   [g] global (everywhere)   esc cancel")))
		return append(lines, FrameRule(th, width))
	}
	if d.stage == stageProvider {
		return d.renderProviders(th, width)
	}
	return d.renderModels(th, width)
}

func (d *ModelDialog) renderProviders(th tui.Theme, width int) []string {
	lines := []string{FrameHeader(th, i18n.T("model · provider"), width)}
	hint := i18n.T("pick a provider (↑/↓, enter, esc) - type to filter")
	if d.provQuery != "" {
		hint = i18n.T("filter: %s", d.provQuery)
	}
	lines = append(lines, th.FG256(th.Muted, hint))

	rows := d.filteredProviders()
	if len(rows) == 0 {
		msg := "  " + i18n.T("no providers match %q", d.provQuery)
		if len(d.providers) == 0 {
			msg = "  " + i18n.T("no credentials found - run /login to add an api key or subscription")
		}
		lines = append(lines, th.FG256(th.Muted, msg))
		return append(lines, FrameRule(th, width))
	}
	if d.provCursor >= len(rows) {
		d.provCursor = len(rows) - 1
	}
	for i, r := range rows {
		plain := fmt.Sprintf("  %-22s %4d", r.label, r.count)
		if r.hidden > 0 {
			plain += i18n.T("  (%d hidden)", r.hidden)
		}
		if i == d.provCursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	return append(lines, FrameRule(th, width))
}

func (d *ModelDialog) renderModels(th tui.Theme, width int) []string {
	header := i18n.T("model")
	if d.scopeLabel != "" {
		header = i18n.T("model · %s", d.scopeLabel)
	}
	lines := []string{FrameHeader(th, header, width)}

	back := i18n.T("esc")
	if !d.single {
		back = i18n.T("esc back")
	}
	base := i18n.T("↑/↓, enter, ctrl+f favorite, ctrl+k hide, ctrl+e edit, ctrl+d set-default, %s - type to filter, :img/:thinking/:hidden", back)
	lines = append(lines, th.FG256(th.Muted, d.p.hintLine(base)))

	if len(d.p.view) == 0 {
		msg := "  " + i18n.T("no models match %q", d.p.query)
		if len(d.p.all) == 0 {
			msg = "  " + i18n.T("(no models in this scope)")
		}
		lines = append(lines, th.FG256(th.Muted, msg))
		return append(lines, FrameRule(th, width))
	}
	lines = append(lines, d.p.renderRows(th, width)...)
	return append(lines, FrameRule(th, width))
}
