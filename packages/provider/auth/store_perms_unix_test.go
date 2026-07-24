//go:build unix

package auth

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestStoreCreatesOwnerOnlyCredentialDir proves that saving credentials creates
// the auth.json directory owner-only (0700) and the file 0600 even under a fully
// permissive umask (0000). Before this hardening the directory was created 0755,
// so under a lax umask the credential root was traversable by other local users
// even though auth.json itself stayed 0600.
func TestStoreCreatesOwnerOnlyCredentialDir(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := filepath.Join(testsupport.TempDir(t), "credroot")
	store := NewStore(filepath.Join(dir, "auth.json"))
	if err := store.SetAPIKey("groq", "gsk_secret_value"); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("credential dir mode = %#o, want 0700 (must not rely on the umask)", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("auth.json mode = %#o, want 0600", got)
	}
}
