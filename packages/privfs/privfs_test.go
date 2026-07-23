package privfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode assertions do not apply on Windows")
	}
}

func TestWriteFilePrivateAndAtomic(t *testing.T) {
	skipOnWindows(t)
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "sub", "config.json")

	if err := WriteFile(p, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != FileMode {
		t.Errorf("file mode = %#o, want %#o", got, FileMode)
	}
	di, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != DirMode {
		t.Errorf("created dir mode = %#o, want %#o", got, DirMode)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"x":1}` {
		t.Errorf("content = %q, want %q", b, `{"x":1}`)
	}

	// Overwrite: still 0600, new content, no leftover temp files.
	if err := WriteFile(p, []byte(`{"x":2}`)); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(p)
	if got := fi.Mode().Perm(); got != FileMode {
		t.Errorf("mode after overwrite = %#o, want %#o", got, FileMode)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"x":2}` {
		t.Errorf("content after overwrite = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config.json.tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestRepairTreePreservesOwnerExecute(t *testing.T) {
	skipOnWindows(t)
	root := filepath.Join(testsupport.TempDir(t), "home")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Force a known starting state independent of the test umask.
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	conf := write("config.json", 0o644) // world/group-readable secret
	bin := write("ext.bin", 0o755)      // an extension binary: must keep +x
	priv := write("already.txt", 0o600) // already private

	changed, err := RepairTree(root)
	if err != nil {
		t.Fatal(err)
	}
	// root (0755) + config (0644) + bin (0755) = 3; priv (0600) untouched.
	if changed != 3 {
		t.Errorf("changed = %d, want 3", changed)
	}
	assertMode := func(p string, want os.FileMode) {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %#o, want %#o", filepath.Base(p), got, want)
		}
	}
	assertMode(root, 0o700)
	assertMode(conf, 0o600)
	assertMode(bin, 0o700) // owner execute preserved, group/other cleared
	assertMode(priv, 0o600)
}

func TestRepairOnceIsGatedByMarker(t *testing.T) {
	skipOnWindows(t)
	home := filepath.Join(testsupport.TempDir(t), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	// Force a known permissive starting state independent of the test umask:
	// under a restrictive umask (a hardened service runs UMask=0077) both the
	// dir and the file would already be private, leaving RepairOnce nothing to
	// tighten and the assertion below vacuously false.
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := RepairOnce(home)
	if err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Errorf("first RepairOnce changed nothing; expected the 0644 config (and home) to be tightened")
	}
	marker := filepath.Join(home, repairMarkerName)
	if fi, err := os.Stat(marker); err != nil {
		t.Errorf("marker not written: %v", err)
	} else if got := fi.Mode().Perm(); got != FileMode {
		t.Errorf("marker mode = %#o, want %#o", got, FileMode)
	}

	// Second run is a no-op even if a new permissive file appears.
	if err := os.WriteFile(filepath.Join(home, "late.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, "late.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err = RepairOnce(home)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("second RepairOnce changed %d entries, want 0 (gated by marker)", changed)
	}
}

func TestRepairOnceNoHomeIsNoop(t *testing.T) {
	skipOnWindows(t)
	missing := filepath.Join(testsupport.TempDir(t), "does-not-exist")
	changed, err := RepairOnce(missing)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("RepairOnce on a missing home changed %d, want 0", changed)
	}
	if _, err := os.Stat(filepath.Join(missing, repairMarkerName)); err == nil {
		t.Errorf("marker should not be written when home does not exist")
	}
}
