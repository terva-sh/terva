package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
)

func boolPtr(b bool) *bool { return &b }

// writeExtensionManifest drops $TERVA_HOME/extensions/<name>/extension.json
// with the given config schema, so the disk-reading helpers can find it.
func writeExtensionManifest(t *testing.T, home, name string, schema []extdriver.ConfigField) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := extdriver.Manifest{Name: name, Exec: "./" + name, Enabled: boolPtr(true), Config: schema}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampleSchema() []extdriver.ConfigField {
	return []extdriver.ConfigField{
		{Key: "api_key", Type: "secret", Required: true},
		{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}, Default: "celsius"},
	}
}

// resolveExtensionConfig overlays stored values on schema defaults, and is
// schema-driven (only declared keys come back).
func TestResolveExtensionConfigDefaultsAndStored(t *testing.T) {
	withTempHome(t)
	writeUserConfig(t, config.TervaHome(), config.Config{
		Extensions: map[string]map[string]json.RawMessage{
			"weather": {"units": json.RawMessage(`"fahrenheit"`)},
		},
	})
	got := build.ResolveExtensionConfig("weather", sampleSchema())
	// Stored value wins for units; api_key has no default/stored so absent.
	if string(got["units"]) != `"fahrenheit"` {
		t.Errorf("units = %s, want fahrenheit", got["units"])
	}
	if _, ok := got["api_key"]; ok {
		t.Errorf("api_key has no default/stored value; should be absent, got %s", got["api_key"])
	}

	// With nothing stored, the default is delivered.
	writeUserConfig(t, config.TervaHome(), config.Config{})
	got = build.ResolveExtensionConfig("weather", sampleSchema())
	if string(got["units"]) != `"celsius"` {
		t.Errorf("default units = %s, want celsius", got["units"])
	}
}

func TestResolveExtensionConfigDropsStaleKeys(t *testing.T) {
	withTempHome(t)
	writeUserConfig(t, config.TervaHome(), config.Config{
		Extensions: map[string]map[string]json.RawMessage{
			"weather": {
				"units":   json.RawMessage(`"celsius"`),
				"old_key": json.RawMessage(`"x"`), // no longer in the schema
			},
		},
	})
	got := build.ResolveExtensionConfig("weather", sampleSchema())
	if _, ok := got["old_key"]; ok {
		t.Errorf("stale key should be dropped from delivery, got %s", got["old_key"])
	}
}

func TestResolveExtensionConfigNoSchema(t *testing.T) {
	withTempHome(t)
	if got := build.ResolveExtensionConfig("x", nil); got != nil {
		t.Errorf("no schema should resolve to nil, got %v", got)
	}
}

func TestSetExtensionConfigValuesRoundTrip(t *testing.T) {
	withTempHome(t)
	writeUserConfig(t, config.TervaHome(), config.Config{FavoriteModels: []string{"anthropic/opus"}})

	err := setExtensionConfigValues("weather", map[string]json.RawMessage{
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
	if err := setExtensionConfigValues("weather", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := config.LoadConfig(); c.Extensions["weather"] != nil {
		t.Errorf("cleared config should remove the block, got %v", c.Extensions["weather"])
	}
}

// typeExtensionConfigValues types per-field and keeps a blank secret.
func TestTypeExtensionConfigValues(t *testing.T) {
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
	out := typeExtensionConfigValues(schema, values, existing)
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
	writeExtensionManifest(t, home, "weather", sampleSchema())
	writeUserConfig(t, home, config.Config{
		Extensions: map[string]map[string]json.RawMessage{
			"weather": {"api_key": json.RawMessage(`"sk-secret"`)},
		},
	})
	fields := extensionConfigFields("", "weather")
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
		Enabled: boolPtr(true),
		Config:  []extdriver.ConfigField{{Key: "roots", Label: "Vault roots", Type: "string"}},
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	fields := extensionConfigFields("", "obsidian")
	if len(fields) != 1 || fields[0].Key != "roots" {
		t.Fatalf(`extensionConfigFields("", "obsidian") = %+v; want one field keyed "roots"`, fields)
	}
}
