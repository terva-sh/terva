package build

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/testsupport"
)

// An extension loaded by path (`--ext /opt/vendor/thing`) declares a schema and
// must be offered its form.
//
// It was not, and the reason is worth stating because it is the third time this
// exact shape has bitten: the schema was right there in the manifest and the
// dialog said there was none. The inventory scan walks two install roots,
// reading each manifest and setting HasConfig from it; a --ext load is outside
// both by construction, so a second pass surfaces it from the live manager —
// and that pass carried name, version, language, description, log path and
// readiness, but not the config schema. HasConfig kept its zero value, and
// every --ext extension reported "no configurable settings" whatever its
// manifest said.
//
// This matters far beyond a dev convenience: a managed deployment vendors its
// extensions precisely this way, so on those hosts NO extension could be
// configured from any surface at all.

func TestAnExtensionLoadedByPathIsOfferedItsForm(t *testing.T) {
	withTempHome(t)

	// Somewhere no install-root scan will ever look.
	vendored := filepath.Join(testsupport.TempDir(t), "vendor", "weather")
	writeExtManifest(t, vendored, "weather", extSampleSchema())

	// Resolving by NAME cannot find it — that is the whole problem, and it is
	// still true, which is why the directory has to be carried instead.
	if got := ExtensionConfigSchema("", "weather"); len(got) != 0 {
		t.Fatalf("a name search should not find a vendored extension; got %d fields", len(got))
	}

	// Resolving by DIRECTORY does.
	schema := ExtensionConfigSchemaIn(vendored)
	if len(schema) != 2 {
		t.Fatalf("ExtensionConfigSchemaIn(%q) = %d fields, want 2 — the manifest declares them", vendored, len(schema))
	}
	form := ExtensionConfigFormIn(vendored, "weather")
	if len(form) != 2 {
		t.Fatalf("the form is empty for an extension that declares a schema: %+v", form)
	}

	// And the secret rule holds on this path too — it is the path a deployed
	// fleet actually uses, so it is the one that most needs to be right.
	for _, f := range form {
		if f.Key == "api_key" && !f.Secret {
			t.Error("api_key lost its secret flag when resolved by directory")
		}
	}
}

// A directory that holds no manifest, or none at all, yields nothing rather
// than panicking — the row exists for extensions that are not running, and
// those carry no directory.
func TestConfigFormToleratesAMissingDirectory(t *testing.T) {
	withTempHome(t)
	if got := ExtensionConfigSchemaIn(""); got != nil {
		t.Errorf("empty dir should yield no schema, got %+v", got)
	}
	if got := ExtensionConfigSchemaIn(filepath.Join(testsupport.TempDir(t), "nope")); got != nil {
		t.Errorf("absent dir should yield no schema, got %+v", got)
	}
}

// The typing rules are the host's, and the one that carries the most weight is
// the secret: a blank submit must keep the stored value rather than clear it.
// That is what lets a client edit every other field while never holding the
// secret — and therefore what lets the secret stay off the wire entirely.
func TestABlankSecretKeepsTheStoredValue(t *testing.T) {
	schema := []extdriver.ConfigField{
		{Key: "api_key", Type: "secret"},
		{Key: "units", Type: "string"},
	}
	out := TypeExtensionConfigValues(schema,
		map[string]string{"api_key": "", "units": "celsius"},
		map[string]json.RawMessage{"api_key": json.RawMessage(`"sk-live"`)})
	if string(out["api_key"]) != `"sk-live"` {
		t.Errorf("a blank secret cleared the stored value: %s", out["api_key"])
	}
	if string(out["units"]) != `"celsius"` {
		t.Errorf("units = %s, want celsius", out["units"])
	}

	// A non-blank secret replaces it, so rotating a key still works.
	out = TypeExtensionConfigValues(schema,
		map[string]string{"api_key": "sk-new"},
		map[string]json.RawMessage{"api_key": json.RawMessage(`"sk-live"`)})
	if string(out["api_key"]) != `"sk-new"` {
		t.Errorf("a submitted secret did not replace the stored one: %s", out["api_key"])
	}
}
