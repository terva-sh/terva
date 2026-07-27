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
