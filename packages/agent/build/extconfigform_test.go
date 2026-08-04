package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/testsupport"
)

// The extension config form and its typing rules. These moved here from the CLI
// when the form went onto the wire: they are the host's rules now, applied once
// for every surface, rather than the in-process terminal's.

func extBoolPtr(b bool) *bool { return &b }

// writeExtManifest drops an extension.json with the given schema at dir.
func writeExtManifest(t *testing.T, dir, name string, schema []extdriver.ConfigField) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := extdriver.Manifest{Name: name, Exec: "./" + name, Enabled: extBoolPtr(true), Config: schema}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeInstalledExtManifest puts one under $TERVA_HOME/extensions/<dirName>,
// where the root scan can find it.
func writeInstalledExtManifest(t *testing.T, home, dirName, name string, schema []extdriver.ConfigField) string {
	t.Helper()
	dir := filepath.Join(home, "extensions", dirName)
	writeExtManifest(t, dir, name, schema)
	return dir
}

func extSampleSchema() []extdriver.ConfigField {
	return []extdriver.ConfigField{
		{Key: "api_key", Type: "secret", Required: true},
		{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}, Default: "celsius"},
	}
}

func TestSetExtensionConfigValuesRoundTrip(t *testing.T) {
	withTempHome(t)
	writeUserConfig(t, config.TervaHome(), config.Config{FavoriteModels: []string{"anthropic/opus"}})

	err := SetExtensionConfigValues("weather", map[string]json.RawMessage{
		"api_key": json.RawMessage(`"sk-123"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := config.LoadConfig()
	if string(c.Extensions["weather"]["api_key"]) != `"sk-123"` {
		t.Fatalf("stored api_key = %s", c.Extensions["weather"]["api_key"])
	}
	if len(c.FavoriteModels) != 1 {
		t.Errorf("unrelated field dropped: favorites=%v", c.FavoriteModels)
	}

	// Clearing removes the block.
	if err := SetExtensionConfigValues("weather", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := config.LoadConfig(); c.Extensions["weather"] != nil {
		t.Errorf("cleared config should remove the block, got %v", c.Extensions["weather"])
	}
}

// typeExtensionConfigValues types per-field and keeps a blank secret.
func TestTypeExtensionConfigValues(t *testing.T) {
	// A home of its own: with at-rest encryption configured, a submitted
	// secret is SEALED, and this test asserts the cleartext it typed.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	schema := []extdriver.ConfigField{
		{Key: "api_key", Type: "secret"},
		{Key: "flag", Type: "bool"},
		{Key: "n", Type: "int"},
		{Key: "units", Type: "select"},
		{Key: "note", Type: "string"},
	}
	existing := map[string]json.RawMessage{"api_key": json.RawMessage(`"old-secret"`)}
	values := map[string]string{
		"api_key": "",           // blank secret → keep existing
		"flag":    "true",       // bool
		"n":       "5",          // int
		"units":   "fahrenheit", // select → string
		"note":    "",           // blank non-secret → omitted
	}
	out, err := TypeExtensionConfigValues("weather", schema, values, existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["api_key"]) != `"old-secret"` {
		t.Errorf("blank secret should keep existing, got %s", out["api_key"])
	}
	if string(out["flag"]) != "true" {
		t.Errorf("flag = %s, want true", out["flag"])
	}
	if string(out["n"]) != "5" {
		t.Errorf("n = %s, want 5", out["n"])
	}
	if string(out["units"]) != `"fahrenheit"` {
		t.Errorf("units = %s, want \"fahrenheit\"", out["units"])
	}
	if _, ok := out["note"]; ok {
		t.Errorf("blank non-secret should be omitted, got %s", out["note"])
	}
}

// extensionConfigFields masks secrets (HasSaved, never Saved) and exposes
// the default hint.
func TestExtensionConfigFieldsMasksSecret(t *testing.T) {
	home := withTempHome(t)
	writeInstalledExtManifest(t, home, "weather", "weather", extSampleSchema())
	writeUserConfig(t, home, config.Config{
		Extensions: map[string]map[string]json.RawMessage{
			"weather": {"api_key": json.RawMessage(`"sk-secret"`)},
		},
	})
	fields := ExtensionConfigForm(nil, "", "weather")
	if len(fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(fields))
	}
	var api, units bool
	for _, f := range fields {
		switch f.Key {
		case "api_key":
			api = true
			if !f.Secret || !f.HasSaved || f.Saved != "" {
				t.Errorf("secret field leaked or mis-flagged: %+v", f)
			}
		case "units":
			units = true
			if f.Default != "celsius" {
				t.Errorf("units default = %q, want celsius", f.Default)
			}
		}
	}
	if !api || !units {
		t.Fatalf("missing fields: api=%v units=%v", api, units)
	}
}

// extensionConfigFields (and thus the /extensions config dialog) must surface
// an extension's schema even when its install-dir basename differs from the
// manifest name — the exact case that made obsidian (dir "terva-ext-obsidian",
// manifest "obsidian") report "no configurable settings" despite declaring
// one. Resolution routes through matchExtensionDir's manifest-name fallback,
// shared with the CLI's findExtensionDir.
func TestExtensionConfigFieldsResolvesManifestNamedDir(t *testing.T) {
	home := withTempHome(t)
	// Install dir keeps the source repo name; the manifest declares a
	// different name (what `ext list` and the dialog key on).
	dir := filepath.Join(home, "extensions", "terva-ext-obsidian")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := extdriver.Manifest{
		Name:    "obsidian",
		Exec:    "./run.sh",
		Enabled: extBoolPtr(true),
		Config:  []extdriver.ConfigField{{Key: "roots", Label: "Vault roots", Type: "string"}},
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	fields := ExtensionConfigForm(nil, "", "obsidian")
	if len(fields) != 1 || fields[0].Key != "roots" {
		t.Fatalf(`ExtensionConfigForm(nil, "", "obsidian") = %+v; want one field keyed "roots"`, fields)
	}
}

// NormalizeFormValue is the validation a widget used to provide for free. The
// browser renders a bool as a checkbox and a select as a list, so nothing could
// submit an impossible value until a client without widgets existed.
func TestNormalizeFormValueRefusesWhatAWidgetMadeImpossible(t *testing.T) {
	boolField := ConfigFormField{Key: "sieve", Type: "bool"}
	for _, in := range []string{"true", "TRUE", "yes", "on", "1"} {
		got, err := NormalizeFormValue(boolField, in)
		if err != nil || got != "true" {
			t.Errorf("NormalizeFormValue(bool, %q) = %q, %v; want \"true\", nil", in, got, err)
		}
	}
	for _, in := range []string{"false", "no", "off", "0"} {
		got, err := NormalizeFormValue(boolField, in)
		if err != nil || got != "false" {
			t.Errorf("NormalizeFormValue(bool, %q) = %q, %v; want \"false\", nil", in, got, err)
		}
	}
	if _, err := NormalizeFormValue(boolField, "soon"); err == nil {
		t.Error("a bool field accepted \"soon\"")
	}

	intField := ConfigFormField{Key: "retries", Type: "int"}
	if got, err := NormalizeFormValue(intField, " 7 "); err != nil || got != "7" {
		t.Errorf("NormalizeFormValue(int, \" 7 \") = %q, %v; want \"7\", nil", got, err)
	}
	if _, err := NormalizeFormValue(intField, "7.5"); err == nil {
		t.Error("an int field accepted \"7.5\"")
	}

	sel := ConfigFormField{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}}
	if got, err := NormalizeFormValue(sel, "celsius"); err != nil || got != "celsius" {
		t.Errorf("NormalizeFormValue(select, \"celsius\") = %q, %v", got, err)
	}
	if _, err := NormalizeFormValue(sel, "kelvin"); err == nil {
		t.Error("a select field accepted a value outside its options")
	}
	// A select that declares no options constrains nothing; refusing every
	// value would be worse than taking it.
	open := ConfigFormField{Key: "mode", Type: "select"}
	if got, err := NormalizeFormValue(open, "anything"); err != nil || got != "anything" {
		t.Errorf("NormalizeFormValue(optionless select) = %q, %v", got, err)
	}
	// A string passes through verbatim, whitespace and all.
	str := ConfigFormField{Key: "prefix", Type: "string"}
	if got, err := NormalizeFormValue(str, "  padded  "); err != nil || got != "  padded  " {
		t.Errorf("NormalizeFormValue(string) = %q, %v; want it untouched", got, err)
	}
}

// ClearExtensionConfigKey removes one value and leaves its neighbours — the
// operation a submitted form cannot express, because a blank secret means
// "keep".
func TestClearExtensionConfigKeyLeavesTheNeighbours(t *testing.T) {
	withTempHome(t)
	if err := SetExtensionConfigValues("weather", map[string]json.RawMessage{
		"api_key": json.RawMessage(`"sk-123"`),
		"units":   json.RawMessage(`"fahrenheit"`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := ClearExtensionConfigKey("weather", "api_key"); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	block := c.Extensions["weather"]
	if _, ok := block["api_key"]; ok {
		t.Error("api_key survived the clear")
	}
	if got := string(block["units"]); got != `"fahrenheit"` {
		t.Errorf("units = %s; want the neighbour untouched", got)
	}
	// Clearing the last key drops the whole block rather than leaving {}.
	if err := ClearExtensionConfigKey("weather", "units"); err != nil {
		t.Fatal(err)
	}
	c, err = config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Extensions["weather"]; ok {
		t.Errorf("an emptied block should be dropped, got %v", c.Extensions)
	}
	// An absent key and an absent extension are both no-ops, not errors: a
	// deployment that clears the same key twice should not fail the second time.
	if err := ClearExtensionConfigKey("weather", "units"); err != nil {
		t.Errorf("clearing an already-absent key: %v", err)
	}
	if err := ClearExtensionConfigKey("nothing-here", "units"); err != nil {
		t.Errorf("clearing a key of an unknown extension: %v", err)
	}
}

// SetExtensionConfigFormIn types against the schema at a KNOWN directory — the
// path a --ext load needs, since resolving by name searches roots it is outside
// of by construction.
func TestSetExtensionConfigFormInTypesAgainstTheDirectorysSchema(t *testing.T) {
	home := withTempHome(t)
	dir := filepath.Join(home, "vendored", "weather")
	writeExtManifest(t, dir, "weather", []extdriver.ConfigField{
		{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}},
		{Key: "retries", Type: "int"},
	})
	if err := SetExtensionConfigFormIn(dir, "weather", map[string]string{
		"units": "fahrenheit", "retries": "3",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	block := c.Extensions["weather"]
	if got := string(block["units"]); got != `"fahrenheit"` {
		t.Errorf("units = %s; want a JSON string", got)
	}
	// The int is typed as a NUMBER because the directory's schema said so —
	// which is the whole point of resolving the schema rather than guessing.
	if got := string(block["retries"]); got != "3" {
		t.Errorf("retries = %s; want the JSON number 3", got)
	}
}
