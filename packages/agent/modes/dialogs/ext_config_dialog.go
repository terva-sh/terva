package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// ConfigField is one row of an extension's config form. The host (cli)
// translates the manifest `config` schema + the user's saved values into
// these; this package only edits working strings and emits them on save,
// so modes never touches config.json or JSON typing. Reached with 'c' from
// the /extensions dialog when the selected extension declares a schema.
type ConfigField struct {
	Key         string
	Label       string
	Type        string // "string" (default) | "bool" | "int" | "select" | "secret"
	Description string
	Required    bool
	Secret      bool
	Options     []string
	Saved       string // the user's saved value (display form); "" = unset
	Default     string // the field's default (display form), shown as a hint
	HasSaved    bool   // a secret field currently has a stored value
}

func (f ConfigField) isSecret() bool { return f.Secret || f.Type == "secret" }
func (f ConfigField) isBool() bool   { return f.Type == "bool" }
func (f ConfigField) isSelect() bool { return f.Type == "select" }
func (f ConfigField) isInt() bool    { return f.Type == "int" }

// ExtConfigDialog is a small form over one extension's declared config.
// Every value is a working string; the host types it per the schema on
// save. Secret fields seed empty and are masked — submitting one empty
// keeps the existing stored secret (the host honors that).
type ExtConfigDialog struct {
	active  bool
	name    string
	fields  []ConfigField
	working map[string]string
	cursor  int
	editing bool
	buf     string
	status  string
}

func NewExtConfigDialog() *ExtConfigDialog { return &ExtConfigDialog{} }

// Open builds the form for one extension from its schema + saved values.
func (d *ExtConfigDialog) Open(name string, fields []ConfigField) {
	d.active = true
	d.name = name
	d.fields = fields
	d.cursor = 0
	d.editing = false
	d.buf = ""
	d.status = ""
	d.working = map[string]string{}
	for _, f := range fields {
		switch {
		case f.isSecret():
			d.working[f.Key] = "" // never seed a secret; empty submit keeps it
		case f.isBool():
			d.working[f.Key] = boolSeed(f)
		case f.isSelect():
			d.working[f.Key] = selectSeed(f)
		default:
			d.working[f.Key] = f.Saved
		}
	}
}

func (d *ExtConfigDialog) Active() bool { return d != nil && d.active }

func (d *ExtConfigDialog) Close() {
	d.active = false
	d.editing = false
	d.buf = ""
}

// ExtConfigAction is returned by HandleKey for the overlay host to apply.
// On Save, Values carries every field's working string keyed by field key;
// the host types and persists them (and handles secret-empty-keeps-old).
type ExtConfigAction struct {
	Save   bool
	Close  bool
	Name   string
	Values map[string]string
}

// HandleKey advances the form and returns an action to apply, if any.
func (d *ExtConfigDialog) HandleKey(k tui.Key) ExtConfigAction {
	if !d.Active() {
		return ExtConfigAction{}
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
		return ExtConfigAction{Close: true}
	case tui.KeyEnter:
		d.activateField()
	case tui.KeyRune:
		switch k.Rune {
		case 's':
			return d.save()
		case ' ':
			f := d.fields[d.cursor]
			if f.isBool() || f.isSelect() {
				d.activateField()
			}
		}
	}
	return ExtConfigAction{}
}

// activateField toggles a bool, cycles a select, or enters text-edit mode
// for a string/int/secret field.
func (d *ExtConfigDialog) activateField() {
	if len(d.fields) == 0 {
		return
	}
	f := d.fields[d.cursor]
	switch {
	case f.isBool():
		if d.working[f.Key] == "true" {
			d.working[f.Key] = "false"
		} else {
			d.working[f.Key] = "true"
		}
	case f.isSelect():
		d.working[f.Key] = cycleOption(f.Options, d.working[f.Key])
	default:
		d.editing = true
		d.buf = "" // secrets always start empty; others edit from blank then commit
		if !f.isSecret() {
			d.buf = d.working[f.Key]
		}
		d.status = ""
	}
}

func (d *ExtConfigDialog) handleEditKey(k tui.Key) ExtConfigAction {
	f := d.fields[d.cursor]
	switch k.Kind {
	case tui.KeyEnter:
		d.working[f.Key] = strings.TrimSpace(d.buf)
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
		if f.isInt() && (k.Rune < '0' || k.Rune > '9') {
			break // digits only for integer fields
		}
		if k.Rune >= 0x20 && k.Rune < 0x7f {
			d.buf += string(k.Rune)
		}
	}
	return ExtConfigAction{}
}

// save validates required fields and returns the working values for the
// host to type and persist.
func (d *ExtConfigDialog) save() ExtConfigAction {
	for _, f := range d.fields {
		if !f.Required {
			continue
		}
		// Satisfied if the user supplied a value, a default exists, or a
		// secret already has a stored value the empty submit will keep.
		if d.working[f.Key] != "" || f.Default != "" || (f.isSecret() && f.HasSaved) {
			continue
		}
		d.status = i18n.T("required: %s", fieldLabel(f))
		return ExtConfigAction{}
	}
	values := make(map[string]string, len(d.working))
	for k, v := range d.working {
		values[k] = v
	}
	d.Close()
	return ExtConfigAction{Save: true, Name: d.name, Values: values}
}

// Render returns the dialog lines.
func (d *ExtConfigDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("config · %s", d.name), width))
	if len(d.fields) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no configurable settings")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	lines = append(lines, th.FG256(th.Muted, i18n.T("↑/↓ field · enter edit/toggle · s save · esc cancel")))

	for i, f := range d.fields {
		shown := d.fieldDisplay(f)
		if d.editing && i == d.cursor {
			if f.isSecret() {
				shown = strings.Repeat("•", len([]rune(d.buf))) + "▏"
			} else {
				shown = d.buf + "▏"
			}
		}
		plain := fmt.Sprintf("  %-18s %s", fieldLabel(f), shown)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}

	if f := d.fields[d.cursor]; f.Description != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+truncate(f.Description, width-4)))
	}
	if d.status != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.status))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// fieldDisplay is the value shown for a field that isn't being edited.
func (d *ExtConfigDialog) fieldDisplay(f ConfigField) string {
	v := d.working[f.Key]
	switch {
	case f.isSecret():
		if v != "" {
			return "•••• (new)"
		}
		if f.HasSaved {
			return "•••• (set)"
		}
		return "(unset)"
	case f.isBool():
		if v == "true" {
			return "on"
		}
		return "off"
	case f.isSelect():
		if v == "" {
			return "(unset)"
		}
		return v
	default:
		if v == "" {
			if f.Default != "" {
				return i18n.T("(default: %s)", f.Default)
			}
			return i18n.T("(unset)")
		}
		return v
	}
}

// ---- helpers ----

func fieldLabel(f ConfigField) string {
	label := f.Label
	if label == "" {
		label = f.Key
	}
	if f.Required {
		label += "*"
	}
	return label
}

func boolSeed(f ConfigField) string {
	if f.Saved != "" {
		return f.Saved
	}
	if f.Default != "" {
		return f.Default
	}
	return "false"
}

func selectSeed(f ConfigField) string {
	if f.Saved != "" {
		return f.Saved
	}
	return f.Default
}

// cycleOption returns the option after cur (wrapping); cur not found or
// empty starts at the first option.
func cycleOption(options []string, cur string) string {
	if len(options) == 0 {
		return cur
	}
	for i, o := range options {
		if o == cur {
			return options[(i+1)%len(options)]
		}
	}
	return options[0]
}
