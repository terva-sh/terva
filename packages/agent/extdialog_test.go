package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/modes"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func disableList(t *testing.T, m map[string]any) []string {
	t.Helper()
	arr, ok := m["disable_extensions"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		out = append(out, e.(string))
	}
	return out
}

// setProjectExtensionDisabled creates the project config, adds/removes a
// name in disable_extensions, preserves unrelated fields, and drops the
// key when the list empties.
func TestSetProjectExtensionDisabled(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".terva", "config.json")

	// Disable "foo" from scratch — file + key created.
	if err := setProjectExtensionDisabled(cwd, "foo", true); err != nil {
		t.Fatal(err)
	}
	if got := disableList(t, readJSON(t, path)); len(got) != 1 || got[0] != "foo" {
		t.Fatalf("after disable foo: %v", got)
	}

	// Seed an unrelated field, then disable "bar" — both survive.
	m := readJSON(t, path)
	m["context_files"] = []any{"NOTES.md"}
	b, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(path, b, 0o644)

	if err := setProjectExtensionDisabled(cwd, "bar", true); err != nil {
		t.Fatal(err)
	}
	m = readJSON(t, path)
	if got := disableList(t, m); len(got) != 2 {
		t.Errorf("want [foo bar], got %v", got)
	}
	if _, ok := m["context_files"]; !ok {
		t.Error("unrelated field context_files was dropped")
	}

	// Re-enabling "foo" removes only it.
	if err := setProjectExtensionDisabled(cwd, "foo", false); err != nil {
		t.Fatal(err)
	}
	if got := disableList(t, readJSON(t, path)); len(got) != 1 || got[0] != "bar" {
		t.Errorf("after enable foo: %v", got)
	}

	// Re-enabling the last one drops the key entirely.
	if err := setProjectExtensionDisabled(cwd, "bar", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := readJSON(t, path)["disable_extensions"]; ok {
		t.Error("emptied disable_extensions should be removed")
	}
}

// Re-disabling an already-disabled name is idempotent (no duplicate).
func TestSetProjectExtensionDisabledIdempotent(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".terva", "config.json")
	for range 3 {
		if err := setProjectExtensionDisabled(cwd, "foo", true); err != nil {
			t.Fatal(err)
		}
	}
	if got := disableList(t, readJSON(t, path)); len(got) != 1 {
		t.Errorf("idempotent disable should yield one entry, got %v", got)
	}
}

// setManifestEnabled flips the enabled flag and preserves other manifest
// fields.
func TestSetManifestEnabled(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "extension.json")
	os.WriteFile(mf, []byte(`{"name":"x","exec":"./run.sh","enabled":true,"description":"keep me"}`), 0o644)

	if err := setManifestEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, mf)
	if v, _ := m["enabled"].(bool); v {
		t.Error("enabled should be false")
	}
	if m["description"] != "keep me" || m["exec"] != "./run.sh" {
		t.Errorf("other manifest fields not preserved: %v", m)
	}

	if err := setManifestEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := readJSON(t, mf)["enabled"].(bool); !v {
		t.Error("enabled should be true again")
	}
}

// appendUnrootedExtensions surfaces explicit --ext loads (those the install-
// root scan didn't already emit) as "session"-scoped rows, so a `terva --ext .`
// dev extension appears in /extensions alongside installed ones.
func TestAppendUnrootedExtensions(t *testing.T) {
	// "installed" was already emitted by the root scan; the manager also has
	// a dev extension loaded by path plus a not-yet-ready one.
	seen := map[string]bool{"installed": true}
	out := []modes.ExtInfo{{Name: "installed", Scope: "global"}}
	live := []sessionExt{
		{Name: "installed", Ready: true}, // already seen -> skipped
		{Name: "devext", Language: "go", Version: "0.1.0", Ready: true},
		{Name: "", Ready: true},         // no manifest name -> skipped
		{Name: "warming", Ready: false}, // loaded but not ready yet
	}
	cmd := map[string]int{"devext": 2}
	tool := map[string]int{"devext": 3}

	out = appendUnrootedExtensions(out, seen, live, cmd, tool)

	if len(out) != 3 { // installed (pre-existing) + devext + warming
		t.Fatalf("want 3 rows, got %d: %+v", len(out), out)
	}
	// installed must not be duplicated.
	n := 0
	for _, it := range out {
		if it.Name == "installed" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("installed should not be duplicated, found %d", n)
	}

	dev := out[1]
	if dev.Name != "devext" || dev.Scope != "session" {
		t.Fatalf("devext row wrong scope/name: %+v", dev)
	}
	if !dev.Running || !dev.Effective || !dev.GlobalEnabled {
		t.Errorf("a ready session ext should read on/effective/enabled: %+v", dev)
	}
	if dev.Commands != 2 || dev.Tools != 3 || dev.Language != "go" || dev.Version != "0.1.0" {
		t.Errorf("devext metadata/counts wrong: %+v", dev)
	}

	warming := out[2]
	if warming.Name != "warming" || warming.Running || warming.Effective {
		t.Errorf("a not-ready session ext should read as not running: %+v", warming)
	}
}
