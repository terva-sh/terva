package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ModelEditDialog edits one model's user-overridable settings and
// persists them to $TERVA_HOME/models.json — the highest-precedence
// catalog layer (see provider.applyUserOverrides). It's reached with
// Ctrl+E from the /model picker.
//
// Every field is tri-state. "inherit" keeps the catalog/live default
// and writes NOTHING for that field, so the override stays minimal and
// future default changes still flow through. An explicit value (or
// on/off for a capability) overrides. Reset removes the whole entry,
// returning the model to its defaults after a confirmation.
type ModelEditDialog struct {
	active bool

	prov    string
	modelID string
	header  string

	fields []editField
	cursor int

	editing bool        // typing into the focused text/int field
	buf     tui.LineBuf // edit buffer while editing, with its own cursor

	confirmingReset bool

	// adding turns the edit form into a create form: two required rows on top
	// (the new id, and which provider to file it under), no reset affordance,
	// and a save that emits Add rather than Save.
	adding bool

	// hasOverride is whether models.json already had an entry on open;
	// it gates the reset affordance (nothing to reset otherwise).
	hasOverride bool

	// synthetic is whether the model exists ONLY because of that entry, so
	// removing it deletes the model rather than restoring catalog values.
	// The reset affordance says which of the two it is about to do; the
	// action itself is the same either way.
	synthetic bool

	// existing is the raw models.json entry as loaded on open. save()
	// starts from a copy of it so fields the editor doesn't manage —
	// prices, api, legacy input — survive an edit instead of being
	// dropped by the entry-replacing upsert.
	existing provider.UserModel

	status string // transient inline validation message
}

type editFieldKind int

const (
	fieldText  editFieldKind = iota // free string (base url)
	fieldInt                        // non-negative integer (context, max tokens)
	fieldFloat                      // bounded float (temperature)
	fieldEnum                       // cycles a fixed option list ("" = inherit)
)

// editField is one row of the form. value == "" means "inherit" for every
// kind; a non-empty value is the override.
//
// A capability tri-state used to carry its own set/on pair. It does not need
// one: "on", "off" and "" are three values in one string, and giving them the
// same representation as every other row is what let the hand-written
// capability rows collapse into the registry loop.
type editField struct {
	key     string // logical key matched in save()
	label   string
	kind    editFieldKind
	value   string // override in string form ("" = inherit)
	inherit string // effective default, shown when the field inherits

	// options are the values a fieldEnum cycles through, in order, after
	// "" (inherit). Per-model rather than fixed: the thinking ladder
	// collapses rungs that reach a given model as the same wire value, and
	// offering both halves of a collapsed pair asks the user to choose
	// between two spellings of one choice.
	options []string

	// required drops the empty state. Every registry field treats "" as
	// inherit, but the add form's id and provider have nothing to inherit
	// FROM, so a blank one is not a weaker answer, it is no answer.
	required bool
}

func NewModelEditDialog() *ModelEditDialog { return &ModelEditDialog{} }

// Open builds the form for m. existing is the raw models.json entry for
// this model (hasExisting=false when none), used to pre-fill which
// fields are already overridden so re-editing shows prior choices.
//
// globalReasoning is the raw global thinking level, needed only so the
// default-thinking row can NAME what it inherits. Without it the row could
// say "inherit" but not from where, which is the failure ResolveReasoning's
// comment describes: a surface naming a value that is not deciding anything.
func (d *ModelEditDialog) Open(m provider.Model, existing provider.UserModel, hasExisting bool, globalReasoning string) {
	d.active = true
	d.prov = m.Provider
	d.modelID = m.ID
	d.header = m.Provider + "/" + m.ID
	d.cursor = 0
	d.editing = false
	d.buf.Clear()
	d.confirmingReset = false
	d.hasOverride = hasExisting
	d.synthetic = m.Synthetic
	d.existing = existing
	d.status = ""

	// EVERY field comes from the shared registry, so a new parameter appears
	// here automatically — the capability tri-states included. They used to be
	// appended by hand below this loop, which is why the web, reading the same
	// registry over the wire, never had them at all.
	d.fields = nil
	for _, p := range provider.ModelParams() {
		value := ""
		if hasExisting {
			value = p.Override(existing)
		}
		var options []string
		switch {
		case p.Kind == provider.ParamTriState:
			// A capability is a closed set of two whose empty value means
			// inherit, so it cycles on the same key and renders with the same
			// "inherit (…)" hint as every other closed set.
			options = []string{"on", "off"}
		case p.Options != nil:
			// An enum with nothing to offer does not apply to this model —
			// "no such setting here", which is not the same as "set to off",
			// so the row goes rather than showing an empty picker.
			if options = p.Options(m); len(options) == 0 {
				continue
			}
		}
		inherit := p.Default(m)
		if p.InheritedFrom != nil {
			inherit = p.InheritedFrom(m, globalReasoning)
		}
		d.fields = append(d.fields, editField{
			key:     p.Key,
			label:   p.Label,
			kind:    paramFieldKind(p.Kind),
			value:   value,
			options: options,
			inherit: inherit,
		})
	}
}

// OpenAdd builds the CREATE form, cloned from src.
//
// Clone-from rather than a blank form. A model terva has no catalog row for
// starts with a zero context window, and every value here has to be explicit
// because there is no layer underneath to inherit from. Copying a sibling that
// already works is the cheapest way to get a complete set.
//
// The seed is each param's DEFAULT, not its override. Default is the effective
// value resolved off src; Override is only what models.json pins, and most
// clone sources pin nothing at all. Seeding from Override would hand back a
// form of empty boxes and create a model with no context window, which is the
// failure clone-from exists to avoid.
//
// providers is the reachable set, from build.LoggedInProviders. It defaults to
// src's provider and stays editable: a provider the user is logged into but
// which has no models yet gets no picker row, so pinning the field to the
// source would make that case unreachable.
func (d *ModelEditDialog) OpenAdd(src provider.Model, providers []string, globalReasoning string) {
	d.active = true
	d.adding = true
	d.prov = src.Provider
	d.modelID = ""
	d.header = i18n.T("from %s", src.Provider+"/"+src.ID)
	d.cursor = 0
	d.editing = false
	d.buf.Clear()
	d.confirmingReset = false
	// Nothing exists yet, so there is nothing to reset and nothing to delete.
	d.hasOverride = false
	d.synthetic = false
	d.existing = provider.UserModel{}
	d.status = ""

	if len(providers) == 0 {
		providers = []string{src.Provider}
	}
	d.fields = []editField{
		{key: "id", label: "id", kind: fieldText, required: true},
		{key: "provider", label: "provider", kind: fieldEnum, value: src.Provider, options: providers, required: true},
	}

	for _, p := range provider.ModelParams() {
		var options []string
		switch {
		case p.Kind == provider.ParamTriState:
			options = []string{"on", "off"}
		case p.Options != nil:
			if options = p.Options(src); len(options) == 0 {
				continue
			}
		}
		// Only seed a value the registry will take back. Default and SetOverride
		// are duals for every param today, but a seeded value is never committed
		// through the field editor, so save() would silently drop one that does
		// not parse. An empty box the user can fill beats a value that vanishes.
		seed := p.Default(src)
		if seed != "" {
			var probe provider.UserModel
			if err := p.SetOverride(&probe, seed); err != nil {
				seed = ""
			}
		}
		inherit := p.Default(src)
		if p.InheritedFrom != nil {
			inherit = p.InheritedFrom(src, globalReasoning)
		}
		d.fields = append(d.fields, editField{
			key:     p.Key,
			label:   p.Label,
			kind:    paramFieldKind(p.Kind),
			value:   seed,
			options: options,
			inherit: inherit,
		})
	}
}

// fieldValue returns the current value of the field with key, or "".
func (d *ModelEditDialog) fieldValue(key string) string {
	for _, f := range d.fields {
		if f.key == key {
			return f.value
		}
	}
	return ""
}

// paramFieldKind maps a registry kind onto the editor's field kind.
//
// A tri-state lands on fieldEnum because that is what it is here: a closed set
// of on and off, with the empty value meaning inherit. A list lands on
// fieldText, because a terminal edits a list as the line it already is; its
// Options travel with the row anyway, and Render offers them under the form.
func paramFieldKind(k provider.ParamKind) editFieldKind {
	switch k {
	case provider.ParamEnum, provider.ParamTriState:
		return fieldEnum
	case provider.ParamInt:
		return fieldInt
	case provider.ParamFloat:
		return fieldFloat
	default:
		return fieldText
	}
}

// modelParamsByKey indexes the registry for save/validation lookups.
func modelParamsByKey() map[string]provider.ModelParam {
	out := make(map[string]provider.ModelParam, len(provider.ModelParams()))
	for _, p := range provider.ModelParams() {
		out[p.Key] = p
	}
	return out
}

// Active reports whether the dialog is visible and consuming input.
func (d *ModelEditDialog) Active() bool { return d != nil && d.active }

// Close hides the dialog and clears its transient sub-states.
func (d *ModelEditDialog) Close() {
	d.active = false
	d.editing = false
	d.confirmingReset = false
	d.adding = false
	d.buf.Clear()
}

// modelEditAction is returned by HandleKey for the overlay host to apply.
type modelEditAction struct {
	Save     bool
	Reset    bool
	Close    bool
	Provider string
	ModelID  string
	Entry    provider.UserModel // assembled override on Save, or the new entry on Add
	// Add creates a model rather than editing one. Separate from Save because
	// the host commits it through a different verb, whose guards are the
	// inverse: models.add refuses an id that already resolves.
	Add bool
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *ModelEditDialog) HandleKey(k tui.Key) modelEditAction {
	if !d.Active() {
		return modelEditAction{}
	}
	if d.confirmingReset {
		return d.handleConfirmKey(k)
	}
	if d.editing {
		return d.handleEditKey(k)
	}

	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.fields)-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		d.Close()
		return modelEditAction{Close: true}
	case tui.KeyEnter:
		f := &d.fields[d.cursor]
		if f.kind == fieldEnum {
			cycleField(f)
		} else {
			d.editing = true
			d.buf.Accept = acceptFor(f.kind)
			d.buf.SetValue(f.value)
			d.status = ""
		}
	case tui.KeyRune:
		switch k.Rune {
		case 's':
			return d.save()
		case 'r':
			if d.hasOverride {
				d.confirmingReset = true
			} else {
				d.status = i18n.T("no custom settings to reset")
			}
		case ' ':
			if f := &d.fields[d.cursor]; f.kind == fieldEnum {
				cycleField(f)
			}
		}
	}
	return modelEditAction{}
}

func (d *ModelEditDialog) handleConfirmKey(k tui.Key) modelEditAction {
	if k.Kind == tui.KeyRune && (k.Rune == 'y' || k.Rune == 'Y') {
		d.Close()
		return modelEditAction{Reset: true, Provider: d.prov, ModelID: d.modelID}
	}
	if k.Kind == tui.KeyEsc || (k.Kind == tui.KeyRune && (k.Rune == 'n' || k.Rune == 'N')) {
		d.confirmingReset = false
	}
	return modelEditAction{}
}

func (d *ModelEditDialog) handleEditKey(k tui.Key) modelEditAction {
	f := &d.fields[d.cursor]
	switch k.Kind {
	case tui.KeyEnter:
		v := strings.TrimSpace(d.buf.Value())
		// Validate through the registry (the single validation authority) and
		// read the value back canonicalized (trims, reformats a float).
		if p, ok := modelParamsByKey()[f.key]; ok {
			var probe provider.UserModel
			if err := p.SetOverride(&probe, v); err != nil {
				d.status = err.Error()
				return modelEditAction{}
			}
			v = p.Override(probe)
		}
		f.value = v
		d.editing = false
		d.buf.Clear()
		d.status = ""
	case tui.KeyEsc:
		d.editing = false
		d.buf.Clear()
	default:
		// Every other key is the buffer's: typing, cursor movement, the kills.
		// The row's Accept, set when editing began, keeps a digits-only field
		// digits-only.
		d.buf.HandleKey(k)
	}
	return modelEditAction{}
}

// acceptFor is the rune filter a field kind types with. A free-text row takes
// anything printable, so it has no filter.
func acceptFor(kind editFieldKind) func(rune) bool {
	switch kind {
	case fieldInt:
		return acceptDigits
	case fieldFloat:
		return acceptDecimal
	default:
		return nil
	}
}

// save merges the form's managed fields onto a copy of the existing
// entry and returns a Save action. Editor-managed fields are set when
// overridden and cleared when inherited; everything else the editor
// doesn't touch (prices, api, legacy input) carries over untouched.
func (d *ModelEditDialog) save() modelEditAction {
	if d.adding {
		return d.saveAdd()
	}
	um := d.existing
	um.ID = d.modelID
	// Copy the capability map before anything writes through it. UserModel is
	// a value, but the assignment above shares its map, and a capability
	// param sets and deletes keys in place — so without this a save would
	// reach back into the entry the dialog loaded.
	if d.existing.Capabilities != nil {
		caps := make(map[string]bool, len(d.existing.Capabilities))
		for k, v := range d.existing.Capabilities {
			caps[k] = v
		}
		um.Capabilities = caps
	}

	// One loop over the registry, and no per-key switch. Every field the form
	// shows was declared, so the dialog no longer knows what any of them mean.
	params := modelParamsByKey()
	for _, f := range d.fields {
		if p, ok := params[f.key]; ok {
			// Values were validated when the field was committed; a blank
			// clears the override. Ignore the (already-vetted) error here.
			_ = p.SetOverride(&um, f.value)
		}
	}

	d.Close()
	return modelEditAction{Save: true, Provider: d.prov, ModelID: d.modelID, Entry: um}
}

// saveAdd assembles the new entry and returns an Add action.
//
// It refuses here rather than at the daemon for the two rows the daemon cannot
// see the point of: a form that bounces back from the wire has already closed,
// and the operator would retype everything. The daemon still refuses both, plus
// the two this cannot know (a duplicate id, an unreachable provider).
func (d *ModelEditDialog) saveAdd() modelEditAction {
	id := strings.TrimSpace(d.fieldValue("id"))
	if id == "" {
		d.status = i18n.T("a model id is required")
		return modelEditAction{}
	}
	prov := strings.TrimSpace(d.fieldValue("provider"))
	if prov == "" {
		d.status = i18n.T("a provider is required")
		return modelEditAction{}
	}
	// A synthetic model has no catalog row beneath it, so a blank window is not
	// "inherit", it is zero. Zero is safe but inert: no gauge, and auto-condensing
	// never fires, so the session grows until the provider refuses the request.
	// The clone seeds a real value, so this only catches a cleared box.
	if w := strings.TrimSpace(d.fieldValue("contextWindow")); w == "" || w == "0" {
		d.status = i18n.T("a context window is required, or this model never auto-condenses")
		return modelEditAction{}
	}

	um := provider.UserModel{ID: id}
	params := modelParamsByKey()
	for _, f := range d.fields {
		if p, ok := params[f.key]; ok {
			_ = p.SetOverride(&um, f.value)
		}
	}

	d.Close()
	return modelEditAction{Add: true, Provider: prov, ModelID: id, Entry: um}
}

// Render returns the dialog lines.
func (d *ModelEditDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	title := i18n.T("edit · %s", d.header)
	if d.adding {
		title = i18n.T("add model · %s", d.header)
	}
	lines = append(lines, FrameHeader(th, title, width))

	if d.confirmingReset {
		// Same key, same models.json write, two different outcomes. On a
		// catalog row the entry is a tweak and removing it restores the
		// shipped values; on a synthetic model the entry IS the model, so
		// there is no default underneath to fall back to. Saying "reset to
		// defaults" over the second one offers something that cannot happen.
		if d.synthetic {
			lines = append(lines, th.FG256(th.Warning, "  "+i18n.T("delete %s?", d.header)))
			lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("it exists only in models.json, so it leaves the picker · y = delete · n/esc = keep")))
		} else {
			lines = append(lines, th.FG256(th.Warning, "  "+i18n.T("reset all custom settings for %s?", d.header)))
			lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("removes its models.json entry · y = reset · n/esc = keep")))
		}
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	hint := i18n.T("↑/↓ field · enter edit/toggle · s save · esc cancel")
	if d.hasOverride {
		if d.synthetic {
			hint += " · " + i18n.T("r delete")
		} else {
			hint += " · " + i18n.T("r reset")
		}
	}
	lines = append(lines, th.FG256(th.Muted, hint))

	for i, f := range d.fields {
		shown := d.fieldDisplay(f)
		if d.editing && i == d.cursor {
			shown = d.buf.Render(tui.LineCaret)
		}
		plain := fmt.Sprintf("  %-15s %s", f.label, shown)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}

	// A list row is edited as a line of text, so the values it accepts have to
	// be readable somewhere. Under the form and for the focused row only: on
	// screen exactly when it is useful, and never as permanent clutter.
	if d.cursor < len(d.fields) {
		if f := d.fields[d.cursor]; f.kind == fieldText && len(f.options) > 0 {
			lines = append(lines, th.FG256(th.Muted,
				"  "+i18n.T("values: %s", strings.Join(f.options, ", "))))
		}
	}

	if d.status != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.status))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// fieldDisplay is the value shown for a field that isn't being edited:
// an explicit value, or "inherit (<default>)" when the field inherits.
func (d *ModelEditDialog) fieldDisplay(f editField) string {
	if f.value == "" {
		if f.required {
			// "inherit" would name a fallback this row does not have.
			return i18n.T("(required)")
		}
		return i18n.T("inherit (%s)", f.inherit)
	}
	return f.value
}

// cycleField advances a cyclable field. One entry point so a new cyclable
// kind cannot be wired to enter but not to space, or the other way round.
func cycleField(f *editField) { cycleEnum(f) }

// cycleEnum advances a fieldEnum: inherit -> the options in ladder order
// -> inherit. A value that is no longer offered (a hand-written
// models.json level this model collapses away) lands on inherit rather
// than sticking, so the cycle can always be escaped.
func cycleEnum(f *editField) {
	for i, o := range f.options {
		if o == f.value {
			if i+1 < len(f.options) {
				f.value = f.options[i+1]
			} else if f.required {
				// Wrap to the first option rather than through inherit: a
				// required field has no such state to pass through.
				f.value = f.options[0]
			} else {
				f.value = ""
			}
			return
		}
	}
	if f.value == "" && len(f.options) > 0 {
		f.value = f.options[0]
		return
	}
	if f.required && len(f.options) > 0 {
		f.value = f.options[0]
		return
	}
	f.value = ""
}

// ---- helpers ----
