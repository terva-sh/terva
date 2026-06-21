package modes

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// modelEditDialog edits one model's user-overridable settings and
// persists them to $TERVA_HOME/models.json — the highest-precedence
// catalog layer (see provider.applyUserOverrides). It's reached with
// Ctrl+E from the /model picker.
//
// Every field is tri-state. "inherit" keeps the catalog/live default
// and writes NOTHING for that field, so the override stays minimal and
// future default changes still flow through. An explicit value (or
// on/off for a capability) overrides. Reset removes the whole entry,
// returning the model to its defaults after a confirmation.
type modelEditDialog struct {
	active bool

	prov    string
	modelID string
	header  string

	fields []editField
	cursor int

	editing bool   // typing into the focused text/int field
	buf     string // edit buffer while editing

	confirmingReset bool

	// hasOverride is whether models.json already had an entry on open;
	// it gates the reset affordance (nothing to reset otherwise).
	hasOverride bool

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
	fieldBool                       // tri-state inherit/on/off (a capability)
)

// editField is one row of the form. For text/int fields, value == ""
// means "inherit"; a non-empty value is the override. For bool fields,
// set==false means "inherit" and on carries the override when set.
type editField struct {
	key     string // logical key matched in save()
	label   string
	kind    editFieldKind
	value   string // text/int override (string form; "" = inherit)
	set     bool   // bool: is the capability explicitly overridden?
	on      bool   // bool: the override value when set
	inherit string // effective default, shown when the field inherits
}

func newModelEditDialog() *modelEditDialog { return &modelEditDialog{} }

// Open builds the form for m. existing is the raw models.json entry for
// this model (hasExisting=false when none), used to pre-fill which
// fields are already overridden so re-editing shows prior choices.
func (d *modelEditDialog) Open(m provider.Model, existing provider.UserModel, hasExisting bool) {
	d.active = true
	d.prov = m.Provider
	d.modelID = m.ID
	d.header = m.Provider + "/" + m.ID
	d.cursor = 0
	d.editing = false
	d.buf = ""
	d.confirmingReset = false
	d.hasOverride = hasExisting
	d.existing = existing
	d.status = ""

	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}

	// Scalar fields come from the shared registry, so a new scalar parameter
	// (temperature, top_p, …) appears here automatically. The tri-state
	// capability/bool fields are a different shape and stay explicit.
	d.fields = nil
	for _, p := range provider.ScalarParams() {
		value := ""
		if hasExisting {
			value = p.Override(existing)
		}
		d.fields = append(d.fields, editField{
			key:     p.Key,
			label:   p.Label,
			kind:    scalarFieldKind(p.Kind),
			value:   value,
			inherit: p.Default(m),
		})
	}
	d.fields = append(d.fields,
		editField{
			key: "reasoning", label: "reasoning", kind: fieldBool,
			set:     hasExisting && existing.Reasoning != nil,
			on:      existing.Reasoning != nil && *existing.Reasoning,
			inherit: onOff(m.Has(provider.CapReasoning)),
		},
		editField{
			key: "imageInput", label: "image input", kind: fieldBool,
			set:     hasExisting && capKeySet(existing.Capabilities, "image-input"),
			on:      hasExisting && existing.Capabilities["image-input"],
			inherit: onOff(m.Has(provider.CapImageInput)),
		},
	)
}

// scalarFieldKind maps a provider scalar kind to the editor's field kind.
func scalarFieldKind(k provider.ScalarKind) editFieldKind {
	switch k {
	case provider.ScalarInt:
		return fieldInt
	case provider.ScalarFloat:
		return fieldFloat
	default:
		return fieldText
	}
}

// scalarParamsByKey indexes the registry for save/validation lookups.
func scalarParamsByKey() map[string]provider.ScalarParam {
	out := make(map[string]provider.ScalarParam, len(provider.ScalarParams()))
	for _, p := range provider.ScalarParams() {
		out[p.Key] = p
	}
	return out
}

// Active reports whether the dialog is visible and consuming input.
func (d *modelEditDialog) Active() bool { return d != nil && d.active }

// Close hides the dialog and clears its transient sub-states.
func (d *modelEditDialog) Close() {
	d.active = false
	d.editing = false
	d.confirmingReset = false
	d.buf = ""
}

// modelEditAction is returned by HandleKey for the overlay host to apply.
type modelEditAction struct {
	Save     bool
	Reset    bool
	Close    bool
	Provider string
	ModelID  string
	Entry    provider.UserModel // assembled override on Save
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *modelEditDialog) HandleKey(k tui.Key) modelEditAction {
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
		if f.kind == fieldBool {
			cycleBool(f)
		} else {
			d.editing = true
			d.buf = f.value
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
				d.status = "no custom settings to reset"
			}
		case ' ':
			if f := &d.fields[d.cursor]; f.kind == fieldBool {
				cycleBool(f)
			}
		}
	}
	return modelEditAction{}
}

func (d *modelEditDialog) handleConfirmKey(k tui.Key) modelEditAction {
	if k.Kind == tui.KeyRune && (k.Rune == 'y' || k.Rune == 'Y') {
		d.Close()
		return modelEditAction{Reset: true, Provider: d.prov, ModelID: d.modelID}
	}
	if k.Kind == tui.KeyEsc || (k.Kind == tui.KeyRune && (k.Rune == 'n' || k.Rune == 'N')) {
		d.confirmingReset = false
	}
	return modelEditAction{}
}

func (d *modelEditDialog) handleEditKey(k tui.Key) modelEditAction {
	f := &d.fields[d.cursor]
	switch k.Kind {
	case tui.KeyEnter:
		v := strings.TrimSpace(d.buf)
		// Validate through the registry (the single validation authority) and
		// read the value back canonicalized (trims, reformats a float).
		if p, ok := scalarParamsByKey()[f.key]; ok {
			var probe provider.UserModel
			if err := p.SetOverride(&probe, v); err != nil {
				d.status = err.Error()
				return modelEditAction{}
			}
			v = p.Override(probe)
		}
		f.value = v
		d.editing = false
		d.buf = ""
		d.status = ""
	case tui.KeyEsc:
		d.editing = false
		d.buf = ""
	case tui.KeyBackspace:
		if r := []rune(d.buf); len(r) > 0 {
			d.buf = string(r[:len(r)-1])
		}
	case tui.KeyRune:
		if k.Alt || k.Ctrl {
			break
		}
		if f.kind == fieldInt && (k.Rune < '0' || k.Rune > '9') {
			break // digits only for integer fields
		}
		if f.kind == fieldFloat && !((k.Rune >= '0' && k.Rune <= '9') || k.Rune == '.') {
			break // digits + a decimal point for float fields
		}
		if k.Rune >= 0x20 && k.Rune < 0x7f {
			d.buf += string(k.Rune)
		}
	}
	return modelEditAction{}
}

// save merges the form's managed fields onto a copy of the existing
// entry and returns a Save action. Editor-managed fields are set when
// overridden and cleared when inherited; everything else the editor
// doesn't touch (prices, api, legacy input) carries over untouched.
func (d *modelEditDialog) save() modelEditAction {
	um := d.existing
	um.ID = d.modelID
	// Copy the capability map so we never mutate the loaded entry's map
	// (UserModel is a value, but maps are shared by the assignment above).
	caps := map[string]bool{}
	for k, v := range d.existing.Capabilities {
		caps[k] = v
	}

	scalars := scalarParamsByKey()
	for _, f := range d.fields {
		if p, ok := scalars[f.key]; ok {
			// Values were validated when the field was committed; a blank
			// clears the override. Ignore the (already-vetted) error here.
			_ = p.SetOverride(&um, f.value)
			continue
		}
		switch f.key {
		case "reasoning":
			if f.set {
				on := f.on
				um.Reasoning = &on
			} else {
				um.Reasoning = nil
			}
		case "imageInput":
			if f.set {
				caps["image-input"] = f.on
			} else {
				delete(caps, "image-input")
			}
		}
	}
	if len(caps) == 0 {
		caps = nil
	}
	um.Capabilities = caps

	d.Close()
	return modelEditAction{Save: true, Provider: d.prov, ModelID: d.modelID, Entry: um}
}

// Render returns the dialog lines.
func (d *modelEditDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, "edit · "+d.header, width))

	if d.confirmingReset {
		lines = append(lines, th.FG256(th.Warning, "  reset all custom settings for "+d.header+"?"))
		lines = append(lines, th.FG256(th.Muted, "  removes its models.json entry · y = reset · n/esc = keep"))
		lines = append(lines, frameRule(th, width))
		return lines
	}

	hint := "↑/↓ field · enter edit/toggle · s save · esc cancel"
	if d.hasOverride {
		hint += " · r reset"
	}
	lines = append(lines, th.FG256(th.Muted, hint))

	for i, f := range d.fields {
		shown := d.fieldDisplay(f)
		if d.editing && i == d.cursor {
			shown = d.buf + "▏"
		}
		plain := fmt.Sprintf("  %-15s %s", f.label, shown)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}

	if d.status != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.status))
	}
	lines = append(lines, frameRule(th, width))
	return lines
}

// fieldDisplay is the value shown for a field that isn't being edited:
// an explicit value, or "inherit (<default>)" when the field inherits.
func (d *modelEditDialog) fieldDisplay(f editField) string {
	if f.kind == fieldBool {
		if !f.set {
			return "inherit (" + f.inherit + ")"
		}
		if f.on {
			return "on"
		}
		return "off"
	}
	if f.value == "" {
		return "inherit (" + f.inherit + ")"
	}
	return f.value
}

// ---- helpers ----

// cycleBool advances a tri-state capability field: inherit -> on -> off
// -> inherit.
func cycleBool(f *editField) {
	switch {
	case !f.set:
		f.set, f.on = true, true
	case f.on:
		f.on = false
	default:
		f.set, f.on = false, false
	}
}

func capKeySet(m map[string]bool, key string) bool {
	_, ok := m[key]
	return ok
}
