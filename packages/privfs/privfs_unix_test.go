//go:build unix

package privfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestMkdirAllPrivateUnderPermissiveUmask proves the acceptance directly: under
// a fully permissive umask (0000 — the worst case, looser than the 0002 the note
// observed), a freshly created private directory is still owner-only 0700 and a
// private file is 0600. os.MkdirAll(dir, 0755) would yield 0755 here; privfs
// pins the mode regardless of the caller's umask, so a group/world-accessible
// credential root can never be created by relying on a tight umask.
func TestMkdirAllPrivateUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	// Literal 0700/0600 (not the DirMode/FileMode constants): the acceptance
	// names those modes, and pinning literals keeps this test load-bearing even
	// if someone loosened the constants.
	root := filepath.Join(testsupport.TempDir(t), "secret-home")
	if err := MkdirAll(root); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(root); err != nil {
		t.Fatal(err)
	} else if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("MkdirAll under umask 0: dir mode = %#o, want 0700", got)
	}

	// WriteFile creates the parent 0700 and the secret file 0600 atomically,
	// again independent of the umask.
	p := filepath.Join(root, "nested", "auth.json")
	if err := WriteFile(p, []byte(`{"token":"deadbeef"}`)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Fatal(err)
	} else if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("WriteFile parent under umask 0: dir mode = %#o, want 0700", got)
	}
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("WriteFile under umask 0: file mode = %#o, want 0600", got)
	}
}
