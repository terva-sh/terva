package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
