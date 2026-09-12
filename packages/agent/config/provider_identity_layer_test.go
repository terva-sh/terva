package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// providers.*.client_identity decides what terva tells a third party about who
// is calling. A cloned repository must not make that choice for the operator,
// so the setting lives in the user layer only.
//
// The mechanism is the type: ProjectConfig does not carry the field, and
// ResolveConfig overlays named fields rather than merging documents. This test
// drives the real resolver against a real project file, because "the struct has
// no field" is a claim about the type while this is a claim about behaviour.
//
// trustProject is TRUE here on purpose. A trusted project is the strongest case
// the gate has to survive: if the value leaked, it would leak here.
func TestProjectConfigCannotSetTheClientIdentity(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	cwd := testsupport.TempDir(t)
	path := ProjectConfigPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Written as raw JSON rather than through the ProjectConfig type, because
	// marshalling that type could never produce this key. A hostile or merely
	// optimistic repository writes whatever it likes.
	//
	// disable_extensions is the POSITIVE CONTROL and it is not decoration. It is
	// allowlisted, so it must arrive. Without it, a resolver that never found
	// this file at all — a wrong path, a silently rejected document — would pass
	// every assertion below while measuring nothing.
	const hostile = `{
	  "disable_extensions": ["proof-the-file-was-read"],
	  "providers": { "openai-codex": { "client_identity": "native" } }
	}`
	if err := os.WriteFile(path, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}

	eff := ResolveConfig(cwd, true)

	var readTheFile bool
	for _, name := range eff.Config.DisableExtensions {
		if name == "proof-the-file-was-read" {
			readTheFile = true
		}
	}
	if !readTheFile {
		t.Fatalf("the positive control did not arrive: eff.Config.DisableExtensions = %#v.\n"+
			"ResolveConfig did not read %s, so this test cannot say anything about whether "+
			"client_identity leaks. Fix the fixture before trusting the assertions below.",
			eff.Config.DisableExtensions, path)
	}

	if got := eff.Config.Providers["openai-codex"].ClientIdentity; got != "" {
		t.Errorf("a project config set client_identity to %q through the merged read view.\n"+
			"This setting is impersonation, and a cloned repository must never be able to turn "+
			"it on. Keep it out of ProjectConfig and out of ResolveConfig's overlay.", got)
	}
	if len(eff.Config.Providers) != 0 {
		t.Errorf("eff.Config.Providers = %#v, want empty — the project layer must contribute "+
			"nothing to this map", eff.Config.Providers)
	}
	// The user layer is the only writer, and it said nothing here either.
	if len(eff.User.Providers) != 0 {
		t.Errorf("eff.User.Providers = %#v, want empty", eff.User.Providers)
	}
}

// The other half: the user layer MUST be able to set it, or the setting is
// merely unreachable rather than user-only. Without this, deleting the wiring
// entirely would leave the test above passing.
func TestUserConfigCanSetTheClientIdentity(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := SaveConfig(Config{
		Providers: map[string]ProviderSettings{
			"openai-codex": {ClientIdentity: "native"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	eff := ResolveConfig(testsupport.TempDir(t), true)
	if got := eff.Config.Providers["openai-codex"].ClientIdentity; got != "native" {
		t.Fatalf("user config client_identity = %q, want \"native\" — the operator's own layer "+
			"must reach the provider, or the setting cannot be switched on at all", got)
	}
}

// A structural guard with a clearer failure than the behavioural one. If
// somebody widens ProjectConfig to carry per-provider settings, this names the
// reason that is wrong at the point of the change, rather than leaving them to
// infer it from a resolver test.
func TestProjectConfigDeclaresNoProviderSettings(t *testing.T) {
	rt := reflect.TypeOf(ProjectConfig{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "providers" || f.Name == "Providers" {
			t.Fatalf("ProjectConfig declares %s (json %q). Per-provider settings include "+
				"client_identity, which changes how terva identifies itself to a third party. "+
				"A cloned repository must not own that decision. Widen the user layer instead.",
				f.Name, tag)
		}
	}
}

// The keyword vocabulary is shared with the provider package, and this package
// must not grow a second copy of it. Asserting the round trip through JSON also
// pins the wire name, which is what an operator actually types.
func TestClientIdentityRoundTripsThroughItsWireName(t *testing.T) {
	const doc = `{"providers":{"openai-codex":{"client_identity":"native"}}}`
	var c Config
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Providers["openai-codex"].ClientIdentity; got != "native" {
		t.Fatalf("client_identity did not decode: got %q", got)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"client_identity":"native"`) {
		t.Fatalf("re-encoded document lost the setting: %s", out)
	}
	// omitempty on both levels: a default config must not start writing a
	// providers block into everyone's config.json.
	empty, err := json.Marshal(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "providers") {
		t.Errorf("an empty Config serialises a providers key: %s", empty)
	}
}
