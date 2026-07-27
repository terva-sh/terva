package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/testsupport"
)

// Extension config crossing the wire.
//
// The browser was told an extension HAD settings (has_config) and given no way
// to see or change one, because the schema and the saved values were local-disk
// reads and a browser has no disk. An attached terminal was worse off still:
// wiring it to the local helpers would have read the wrong machine's manifests.
// So a deployed web-only agent had no path to its own configuration at all, and
// moving one boolean meant stopping the service and hand-editing an agent-owned
// config.json as root — a file that stores secret-typed fields in plaintext.
//
// The rule that must survive the crossing: a secret's VALUE never leaves the
// host. Only whether one exists.

func seedExtManifest(t *testing.T, schema []extdriver.ConfigField) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	dir := filepath.Join(home, "extensions", "weather")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	enabled := true
	mf := extdriver.Manifest{Name: "weather", Exec: "./weather", Enabled: &enabled, Config: schema}
	b, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestASecretsValueNeverReachesTheWire(t *testing.T) {
	dir := seedExtManifest(t, []extdriver.ConfigField{
		{Key: "api_key", Type: "secret", Required: true},
		{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}, Default: "celsius"},
	})

	// Both a secret and a plain value are stored.
	if err := config.MutateConfig(func(c *config.Config) {
		c.Extensions = map[string]map[string]json.RawMessage{
			"weather": {
				"api_key": json.RawMessage(`"sk-live-do-not-leak"`),
				"units":   json.RawMessage(`"fahrenheit"`),
			},
		}
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fields := extensionConfigFormFor(t, "weather", dir)
	if len(fields) != 2 {
		t.Fatalf("form = %d fields, want 2", len(fields))
	}

	var api, units ctrlproto.ExtensionConfigField
	for _, f := range fields {
		switch f.Key {
		case "api_key":
			api = f
		case "units":
			units = f
		}
	}
	if !api.Secret || !api.HasSaved {
		t.Errorf("the secret field lost its flags: %+v", api)
	}
	if api.Saved != "" {
		t.Errorf("the secret's VALUE is on the wire: %q", api.Saved)
	}
	if units.Saved != "fahrenheit" {
		t.Errorf("a non-secret saved value should travel, got %q", units.Saved)
	}

	// The decisive check: serialise the whole pane the way the daemon does and
	// grep the bytes. A field-by-field assertion passes if a later edit adds
	// another place the value could ride along; the payload cannot lie.
	view := ctrlproto.ExtensionsView{Extensions: []ctrlproto.ExtensionInfo{{
		Name: "weather", HasConfig: true, Config: fields,
	}}}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sk-live-do-not-leak") {
		t.Fatalf("the stored secret was serialised into the extensions pane:\n%s", raw)
	}
}

// extensionConfigFormFor builds the wire form the way extensionsView does,
// without standing up a whole workspace: the mapping is what is under test.
func extensionConfigFormFor(t *testing.T, name, dir string) []ctrlproto.ExtensionConfigField {
	t.Helper()
	return extensionConfigForm(&wsSession{}, extensions.Info{Name: name, HasConfig: true, Dir: dir})
}
