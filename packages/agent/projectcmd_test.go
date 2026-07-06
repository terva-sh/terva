package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"terva.sh/terva/packages/testsupport"
	"testing"
)

// chdirTemp changes into dir for the test and restores the old cwd afterward.
// (Hand-rolled rather than t.Chdir, which needs go1.24; the module is go1.22.)
func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestAdoptListRoundTripPreservesFields(t *testing.T) {
	dir := testsupport.TempDir(t)
	chdirTemp(t, dir)
	// A pre-existing project config with unrelated fields.
	mustWrite(t, filepath.Join(dir, ".terva", "config.json"),
		`{"project_scoped": true, "disable_mcp": ["x"]}`)

	if err := writeAdoptList([]string{"weather"}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".terva", "config.json"))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["project_scoped"] != true {
		t.Error("writeAdoptList dropped project_scoped")
	}
	if _, ok := m["disable_mcp"]; !ok {
		t.Error("writeAdoptList dropped disable_mcp")
	}
	if got, _ := readAdoptList(); len(got) != 1 || got[0] != "weather" {
		t.Errorf("adopt list = %v, want [weather]", got)
	}

	// Dropping the last one removes the key (no dangling empty array).
	if err := writeAdoptList(nil); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, ".terva", "config.json"))
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if _, ok := m2["adopt_extensions"]; ok {
		t.Error("empty adopt list should remove the key")
	}
	if m2["project_scoped"] != true {
		t.Error("project_scoped lost on the second write")
	}
}

func TestProjectExtAdoptValidatesGlobal(t *testing.T) {
	proj := testsupport.TempDir(t)
	chdirTemp(t, proj)
	global := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", global) // GlobalExtensionsDir resolves here (not scoped)

	// Refuse to adopt an extension that isn't installed globally.
	if err := projectExtAdopt("weather"); err == nil {
		t.Error("adopting an uninstalled global extension should error")
	}

	// Install a fake global extension, then adoption succeeds and records it.
	mustWrite(t, filepath.Join(global, "extensions", "weather", "extension.json"), `{"name":"weather"}`)
	if err := projectExtAdopt("weather"); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got, _ := readAdoptList(); len(got) != 1 || got[0] != "weather" {
		t.Errorf("adopt list = %v, want [weather]", got)
	}

	// Drop removes it again.
	if err := projectExtDrop("weather"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if got, _ := readAdoptList(); len(got) != 0 {
		t.Errorf("after drop, adopt list = %v, want empty", got)
	}
}

func TestProjectInitScaffolds(t *testing.T) {
	dir := testsupport.TempDir(t)
	chdirTemp(t, dir)
	if err := runProjectInit([]string{"--persona", "Data"}); err != nil {
		t.Fatal(err)
	}
	m, _, _ := readProjectConfigMap()
	var scoped bool
	_ = json.Unmarshal(m["project_scoped"], &scoped)
	if !scoped {
		t.Error("init should set project_scoped: true")
	}
	for _, p := range []string{
		".terva/extensions", ".terva/personas",
		".terva/home/.gitignore", ".terva/personas/data.md",
	} {
		if !fileExists(filepath.Join(dir, p)) {
			t.Errorf("init did not create %s", p)
		}
	}
	// Idempotent: a second init doesn't error or clobber.
	if err := runProjectInit(nil); err != nil {
		t.Fatalf("re-init: %v", err)
	}
}

func TestProjectExtDisableEnable(t *testing.T) {
	chdirTemp(t, testsupport.TempDir(t))
	if err := projectExtSetDisabled("noisy", true); err != nil {
		t.Fatal(err)
	}
	if l, _ := readProjectList("disable_extensions"); len(l) != 1 || l[0] != "noisy" {
		t.Errorf("after disable: %v, want [noisy]", l)
	}
	if err := projectExtSetDisabled("noisy", false); err != nil {
		t.Fatal(err)
	}
	if l, _ := readProjectList("disable_extensions"); len(l) != 0 {
		t.Errorf("after enable: %v, want empty", l)
	}
}

func TestProjectScalarSetAndClear(t *testing.T) {
	chdirTemp(t, testsupport.TempDir(t))
	if err := runProjectScalar("model", []string{"opus-x"}); err != nil {
		t.Fatal(err)
	}
	m, _, _ := readProjectConfigMap()
	var v string
	_ = json.Unmarshal(m["model"], &v)
	if v != "opus-x" {
		t.Errorf("model = %q, want opus-x", v)
	}
	if err := runProjectScalar("model", []string{"clear"}); err != nil {
		t.Fatal(err)
	}
	m2, _, _ := readProjectConfigMap()
	if _, ok := m2["model"]; ok {
		t.Error("clear should remove the model key")
	}
}
