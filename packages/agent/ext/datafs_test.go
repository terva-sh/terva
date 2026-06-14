package ext

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDataFSReadThrough: reads prefer the writable upper layer and fall
// through to the read-only install layer; writes always land upper
// (copy-on-write), shadowing the lower copy without mutating it.
func TestDataFSReadThrough(t *testing.T) {
	upper := t.TempDir()
	lower := t.TempDir()
	// A default shipped in the install dir, and a user override in DataDir.
	if err := os.WriteFile(filepath.Join(lower, "config.json"), []byte("default"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lower, "legacy.json"), []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := HostInfo{DataDir: upper, ExtensionDir: lower}.DataFS()

	// Falls through to the install layer when not yet in DataDir — this
	// is both the "ship a default" and the "old data still readable"
	// path (the install dir was the previous DataDir).
	if b, err := fs.ReadFile("legacy.json"); err != nil || string(b) != "old-data" {
		t.Fatalf("fall-through read: got %q err %v", b, err)
	}

	// A write lands upper and shadows the lower default; the lower copy
	// is untouched.
	if err := fs.WriteFile("config.json", []byte("override"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile("config.json"); string(b) != "override" {
		t.Fatalf("upper should win: got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(lower, "config.json")); string(b) != "default" {
		t.Fatalf("lower (read-only) must be untouched: got %q", b)
	}

	if !fs.Exists("legacy.json") || !fs.Exists("config.json") || fs.Exists("missing") {
		t.Fatal("Exists wrong across layers")
	}
}

// TestDataFSRejectsEscape: a name that escapes the layer must be refused.
func TestDataFSRejectsEscape(t *testing.T) {
	fs := HostInfo{DataDir: t.TempDir(), ExtensionDir: t.TempDir()}.DataFS()
	for _, bad := range []string{"../escape", "/etc/passwd", ".."} {
		if _, err := fs.ReadFile(bad); err == nil {
			t.Errorf("ReadFile(%q) should be rejected", bad)
		}
		if err := fs.WriteFile(bad, []byte("x"), 0o644); err == nil {
			t.Errorf("WriteFile(%q) should be rejected", bad)
		}
	}
}

// TestProjectDataDir: scopes under projects/<ProjectID>, creates the dir,
// and uses a shared bucket when there's no active project.
func TestProjectDataDir(t *testing.T) {
	data := t.TempDir()

	dir, err := HostInfo{DataDir: data, ProjectID: "myrepo-abc123"}.ProjectDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(data, "projects", "myrepo-abc123"); dir != want {
		t.Fatalf("got %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("ProjectDataDir not created: %v", err)
	}

	// No project yet -> shared bucket, not an error.
	noproj, err := HostInfo{DataDir: data}.ProjectDataDir()
	if err != nil || filepath.Base(noproj) != "_noproject" {
		t.Fatalf("no-project bucket: got %q err %v", noproj, err)
	}

	// No data dir at all -> error, not a path under "".
	if _, err := (HostInfo{}).ProjectDataDir(); err == nil {
		t.Fatal("expected error when DataDir is empty")
	}
}
