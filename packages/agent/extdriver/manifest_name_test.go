package extdriver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestLoadRejectsUnsafeManifestName: a manifest name that is not a plain path
// element must be rejected before the driver registers it or creates any host
// path (ext-data/<name>, ext-<name>.log), so a crafted "../.." name can't make
// the host write outside its intended directories — even on a load that would
// otherwise spawn (Exec set).
func TestLoadRejectsUnsafeManifestName(t *testing.T) {
	home := testsupport.TempDir(t)
	d := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})

	bad := []string{
		"",
		".",
		"..",
		"../evil",
		"a/b",
		`a\b`,
		"/abs/name",
		"foo/../bar",
	}
	for _, name := range bad {
		err := d.Load(context.Background(), home, Manifest{Name: name, Exec: "/bin/true"})
		if err == nil {
			t.Errorf("Load(name=%q) = nil, want rejection", name)
		}
		if _, ok := d.ext[name]; ok {
			t.Errorf("Load(name=%q) registered the extension despite rejection", name)
		}
	}
	// No host paths should have been created for any rejected name.
	if entries, _ := os.ReadDir(filepath.Join(home, "ext-data")); len(entries) != 0 {
		t.Errorf("ext-data should be empty after rejected loads, got %d entries", len(entries))
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "logs")); len(entries) != 0 {
		t.Errorf("logs should be empty after rejected loads, got %d entries", len(entries))
	}
}

// TestLoadAcceptsPlainManifestName: the guard must not reject legitimate names.
// A theme-only extension (no exec) registers ready without spawning, so this
// exercises the accept path without launching a subprocess.
func TestLoadAcceptsPlainManifestName(t *testing.T) {
	home := testsupport.TempDir(t)
	d := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})

	for _, name := range []string{"my-theme", "todos", "acme.helper", "Ext Name"} {
		if err := d.Load(context.Background(), home, Manifest{Name: name}); err != nil {
			t.Fatalf("Load(plain name %q) = %v, want nil", name, err)
		}
		if _, ok := d.ext[name]; !ok {
			t.Errorf("plain extension %q was not registered", name)
		}
	}
}
