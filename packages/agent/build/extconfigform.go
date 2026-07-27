package build

// An extension's config form: the manifest's declared schema, overlaid with the
// values the user has saved, and the typing rules that turn edited strings back
// into the JSON stored under config.json's extensions.<name>.
//
// This lives in build rather than in the CLI because every surface needs it and
// only one of them shares a filesystem with the host. It used to be host-side
// only — the in-process TUI read the manifest and config.json directly — which
// is why an attached terminal and the browser could see that an extension HAD
// settings and could not see or change one of them.
//
// The types here are deliberately transport-free: the control plane maps them
// onto the wire, the terminal maps them onto its form widget. build stays the
// assembler and does not learn about either.

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/i18n"
)

// ConfigFormField is one row of the form: what the manifest declares, plus what
// the user has saved for it, in display form.
//
// Saved is EMPTY for a secret field — only HasSaved says whether one exists.
// The stored value stays on the host, and a blank secret on submit means "leave
// it alone" (see TypeExtensionConfigValues), so it never needs to travel.
type ConfigFormField struct {
	Key         string
	Label       string
	Type        string // string | bool | int | select | secret
	Description string
	Required    bool
	Secret      bool
	Options     []string
	Saved       string
	Default     string
	HasSaved    bool
}

// ExtensionConfigFormIn builds the form from a KNOWN directory — the form that
// works for a --ext load, whose directory is the only thing that can find it.
func ExtensionConfigFormIn(dir, name string) []ConfigFormField {
	return configForm(ExtensionConfigSchemaIn(dir), name)
}

// ExtensionConfigForm builds one extension's form, resolving its directory by
// name. Empty when the extension declares no schema, or cannot be found at all.
func ExtensionConfigForm(mgr *extensions.Manager, cwd, name string) []ConfigFormField {
	return configForm(ExtensionConfigSchemaFor(mgr, cwd, name), name)
}

func configForm(schema []extdriver.ConfigField, name string) []ConfigFormField {
	if len(schema) == 0 {
		return nil
	}
	var saved map[string]json.RawMessage
	if c, err := config.LoadConfig(); err == nil {
		saved = c.Extensions[name]
	}
	out := make([]ConfigFormField, 0, len(schema))
	for _, f := range schema {
		ff := ConfigFormField{
			Key:         f.Key,
			Label:       f.Label,
			Type:        f.Type,
			Description: f.Description,
			Required:    f.Required,
			Secret:      f.IsSecret(),
			Options:     f.Options,
			Default:     DefaultToDisplay(f.Default),
		}
		if v, ok := saved[f.Key]; ok && len(v) > 0 {
			if ff.Secret {
				ff.HasSaved = true
			} else {
				ff.Saved = RawToDisplay(v)
			}
		}
		out = append(out, ff)
	}
	return out
}

// SetExtensionConfigForm types a submitted form per the extension's schema and
// persists it under config.json's extensions.<name>.
//
// The typing lives here, on the host, rather than in each client: three clients
// deriving "is this field a bool" from the same schema is three chances to
// disagree about it, and the one that got it wrong would write a config the
// extension cannot read.
func SetExtensionConfigForm(mgr *extensions.Manager, cwd, name string, values map[string]string) error {
	return SetExtensionConfigFormIn(ExtensionDirFor(mgr, cwd, name), name, values)
}

// SetExtensionConfigFormIn is the same write from a KNOWN directory — the twin
// of ExtensionConfigFormIn, for the caller that has the extension's path but no
// manager to resolve it (a --ext load pointed at from the command line).
func SetExtensionConfigFormIn(dir, name string, values map[string]string) error {
	schema := ExtensionConfigSchemaIn(dir)
	var existing map[string]json.RawMessage
	if c, err := config.LoadConfig(); err == nil {
		existing = c.Extensions[name]
	}
	return SetExtensionConfigValues(name, TypeExtensionConfigValues(schema, values, existing))
}

// SetExtensionConfigValues writes one extension's saved values into the user
// config.json (extensions.<name>), replacing the whole block; an empty map
// clears it. Secret values are stored plaintext — the UI masks them, and the
// host never logs them.
func SetExtensionConfigValues(name string, values map[string]json.RawMessage) error {
	c, err := config.LoadConfig()
	if err != nil {
		return err
	}
	if len(values) == 0 {
		delete(c.Extensions, name)
		if len(c.Extensions) == 0 {
			c.Extensions = nil
		}
	} else {
		if c.Extensions == nil {
			c.Extensions = map[string]map[string]json.RawMessage{}
		}
		c.Extensions[name] = values
	}
	return config.SaveConfig(c)
}

// ClearExtensionConfigKey removes ONE saved value, leaving the rest of the
// extension's block alone, so the field falls back to its declared default.
//
// A submitted form cannot express this for a secret: a blank secret means "keep
// what is stored" (that is what lets a client edit the other fields without ever
// holding the secret), so every form-shaped path can set a secret and none can
// clear one. Rather than overload blank — the one value a form can send that
// already means something else — a delete is its own operation, and then it is
// the single path for clearing any field rather than a special case for one.
func ClearExtensionConfigKey(name, key string) error {
	c, err := config.LoadConfig()
	if err != nil {
		return err
	}
	block, ok := c.Extensions[name]
	if !ok {
		return nil
	}
	if _, ok := block[key]; !ok {
		return nil
	}
	delete(block, key)
	if len(block) == 0 {
		return SetExtensionConfigValues(name, nil)
	}
	return SetExtensionConfigValues(name, block)
}

// TypeExtensionConfigValues converts a submitted form's strings into the typed
// JSON stored under config.json, driven by the schema: bool and int fields
// become JSON bool/number, the rest JSON strings.
//
// Two rules carry real weight. A blank NON-secret value is omitted, so the
// field falls back to its declared default rather than being pinned to empty. A
// blank SECRET keeps the existing stored value — an empty submit never clears a
// secret, which is what lets a client edit the other fields without ever
// holding the secret, and is why the secret need not be sent to render the form.
func TypeExtensionConfigValues(schema []extdriver.ConfigField, values map[string]string, existing map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range schema {
		v := values[f.Key]
		if f.IsSecret() {
			if v == "" {
				if old, ok := existing[f.Key]; ok && len(old) > 0 {
					out[f.Key] = old
				}
				continue
			}
			out[f.Key] = jsonStringRaw(v)
			continue
		}
		if v == "" {
			continue // unset → the default applies at resolve time
		}
		out[f.Key] = typeScalar(f, v)
	}
	return out
}

// NormalizeFormValue turns one value typed on a command line into the display
// string the form expects, refusing what a widget would have made impossible.
//
// The browser and the terminal dialog render a bool as a checkbox and a select
// as a list, so an out-of-range value cannot be submitted: the widget IS the
// validation, which is why nothing needed this until a client without widgets
// existed. On a command line `set retries=soon` would otherwise sail through
// typeScalar, land in config.json as the JSON string "soon" where the extension
// expects a number, and surface much later as a failure naming neither the key
// nor the command that wrote it. Same constraint, applied where the value
// enters rather than where it breaks.
func NormalizeFormValue(f ConfigFormField, in string) (string, error) {
	switch f.Type {
	case "bool":
		switch strings.ToLower(strings.TrimSpace(in)) {
		case "true", "t", "yes", "y", "on", "1":
			return "true", nil
		case "false", "f", "no", "n", "off", "0":
			return "false", nil
		}
		return "", i18n.Errorf("%s is a bool: want true or false, got %q", f.Key, in)
	case "int":
		n, err := strconv.Atoi(strings.TrimSpace(in))
		if err != nil {
			return "", i18n.Errorf("%s is an int: %q is not a whole number", f.Key, in)
		}
		return strconv.Itoa(n), nil
	case "select":
		// A select with no declared options constrains nothing — take the
		// value rather than refusing every possible one.
		if len(f.Options) == 0 || slices.Contains(f.Options, in) {
			return in, nil
		}
		return "", i18n.Errorf("%s must be one of %s, got %q", f.Key, strings.Join(f.Options, ", "), in)
	}
	return in, nil
}

// typeScalar encodes one submitted string as JSON per the field's type.
func typeScalar(f extdriver.ConfigField, v string) json.RawMessage {
	switch f.Type {
	case "bool":
		if v == "true" {
			return json.RawMessage("true")
		}
		return json.RawMessage("false")
	case "int":
		if n, err := strconv.Atoi(v); err == nil {
			return json.RawMessage(strconv.Itoa(n))
		}
		return jsonStringRaw(v)
	default: // string, select, secret
		return jsonStringRaw(v)
	}
}

func jsonStringRaw(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// RawToDisplay renders a stored JSON value as the form's editable string: a
// JSON string becomes its text, a bool/number its literal.
func RawToDisplay(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// DefaultToDisplay renders a manifest default (decoded as any) as a string.
func DefaultToDisplay(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}
