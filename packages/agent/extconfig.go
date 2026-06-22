package agent

// Host side of extension config templates. The manifest declares a schema
// (extdriver.ConfigField); the user's values live in config.json under
// extensions.<name>; the resolved values (defaults overlaid with the
// user's) are delivered to the extension via the driver's ConfigResolver
// (hello_ack) and a live PushConfigUpdate. config.json access lives here,
// in package agent — the driver stays dependency-light and only delivers.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/modes"
)

// resolveExtensionConfig produces the values to deliver to one extension:
// for every field its manifest declares, the user's saved value if present
// (config.json extensions.<name>.<key>), else the field's default. It is
// schema-driven — only declared keys are delivered, so a stale saved key
// (the field was removed) is kept on disk but dropped from delivery.
// Returns nil when the extension declares no schema or nothing resolves.
// Matches extdriver.ConfigResolver so it wires straight into the driver.
func resolveExtensionConfig(name string, schema []extdriver.ConfigField) map[string]json.RawMessage {
	if len(schema) == 0 {
		return nil
	}
	var stored map[string]json.RawMessage
	if c, err := LoadConfig(); err == nil {
		stored = c.Extensions[name]
	}
	out := map[string]json.RawMessage{}
	for _, f := range schema {
		if v, ok := stored[f.Key]; ok && len(v) > 0 {
			out[f.Key] = v
			continue
		}
		if f.Default != nil {
			if b, err := json.Marshal(f.Default); err == nil {
				out[f.Key] = b
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// setExtensionConfigValues writes one extension's saved values into the
// user config.json (extensions.<name>), replacing the whole block; an
// empty map clears it. Type-safe round-trip through Config (the user layer
// is fully modeled), matching setUserMCPDisabled. Secret values are stored
// plaintext — the UI masks them, the host never logs them.
func setExtensionConfigValues(name string, values map[string]json.RawMessage) error {
	c, err := LoadConfig()
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
	return SaveConfig(c)
}

// extensionConfigSchema reads one installed extension's declared config
// schema from its manifest, without spawning it (so the dialog works for a
// stopped or disabled extension). Returns nil when not found or none.
func extensionConfigSchema(cwd, name string) []extdriver.ConfigField {
	dir, err := findExtensionDirIn(cwd, name)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Config []extdriver.ConfigField `json:"config"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m.Config
}

// extensionConfigFields builds the /extensions config form for one
// extension: a modes.ConfigField per declared schema field, carrying its
// type/label/options, the field default (as a hint), and the user's saved
// value. Secret fields never expose the saved value — only whether one
// exists (HasSaved) — so the dialog masks it.
func extensionConfigFields(cwd, name string) []modes.ConfigField {
	schema := extensionConfigSchema(cwd, name)
	if len(schema) == 0 {
		return nil
	}
	var saved map[string]json.RawMessage
	if c, err := LoadConfig(); err == nil {
		saved = c.Extensions[name]
	}
	out := make([]modes.ConfigField, 0, len(schema))
	for _, f := range schema {
		mf := modes.ConfigField{
			Key:         f.Key,
			Label:       f.Label,
			Type:        f.Type,
			Description: f.Description,
			Required:    f.Required,
			Secret:      f.IsSecret(),
			Options:     f.Options,
			Default:     defaultToDisplay(f.Default),
		}
		if v, ok := saved[f.Key]; ok && len(v) > 0 {
			if mf.Secret {
				mf.HasSaved = true
			} else {
				mf.Saved = rawToDisplay(v)
			}
		}
		out = append(out, mf)
	}
	return out
}

// typeExtensionConfigValues converts the dialog's working strings into the
// typed JSON values stored under config.json, driven by the schema: bool
// and int fields become JSON bool/number, the rest JSON strings. A blank
// non-secret value is omitted (the field falls back to its default). A
// blank secret keeps the existing stored value (an empty submit never
// clears a secret), so the user can edit other fields without re-entering
// it.
func typeExtensionConfigValues(schema []extdriver.ConfigField, values map[string]string, existing map[string]json.RawMessage) map[string]json.RawMessage {
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
			continue // unset → default applies at resolve time
		}
		out[f.Key] = typeScalar(f, v)
	}
	return out
}

// setExtensionConfigFromForm types the dialog's working values per the
// extension's schema and persists them, keeping any existing secret the
// user left blank.
func setExtensionConfigFromForm(cwd, name string, values map[string]string) error {
	schema := extensionConfigSchema(cwd, name)
	var existing map[string]json.RawMessage
	if c, err := LoadConfig(); err == nil {
		existing = c.Extensions[name]
	}
	return setExtensionConfigValues(name, typeExtensionConfigValues(schema, values, existing))
}

// typeScalar encodes one working string as JSON per the field's type.
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

// rawToDisplay renders a stored JSON value as the dialog's editable string:
// a JSON string becomes its text, a bool/number its literal.
func rawToDisplay(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// defaultToDisplay renders a manifest default (decoded as any) as a string.
func defaultToDisplay(v any) string {
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
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// applyExtensionConfigLive delivers the just-saved config to a running
// extension as a config_update event. No-op when the extension is stopped
// or didn't subscribe — its new values still arrive in hello_ack on the
// next spawn.
func applyExtensionConfigLive(mgr *extensions.Manager, cwd, name string) {
	if mgr == nil {
		return
	}
	mgr.PushConfigUpdate(name, resolveExtensionConfig(name, extensionConfigSchema(cwd, name)))
}
