package tasks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode assertions do not apply on Windows")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// TestDirFSWritesPrivately pins the R5 Batch-B fix: boards are user-private
// state, so DirFS must create 0700 directories and 0600 files regardless of
// the perm argument (kept only for FileStore interface compatibility).
func TestDirFSWritesPrivately(t *testing.T) {
	skipOnWindows(t)
	root := filepath.Join(testsupport.TempDir(t), "tasks")
	fs := NewDirFS(root)

	if err := fs.WriteFile("tasks-abc.json", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Join(root, "tasks-abc.json"), 0o600)

	got, err := fs.ReadFile("tasks-abc.json")
	if err != nil || string(got) != `{}` {
		t.Fatalf("roundtrip = %q, %v", got, err)
	}
}

// TestDirFSRepairsLegacyBoardMode pins the migration path: overwriting a board
// an older build wrote 0644 must leave it 0600 (the atomic rename replaces the
// permissive inode).
func TestDirFSRepairsLegacyBoardMode(t *testing.T) {
	skipOnWindows(t)
	root := testsupport.TempDir(t)
	legacy := filepath.Join(root, "tasks-abc.json")
	if err := os.WriteFile(legacy, []byte(`{"tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := NewDirFS(root).WriteFile("tasks-abc.json", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	assertMode(t, legacy, 0o600)
}

// TestLayeredFSMigratesLegacyBoardPrivately pins the full legacy story: a 0644
// board in the lower (old extension) dir loads via fall-through, and the next
// save lands in the upper dir as a private file, leaving the lower copy
// untouched.
func TestLayeredFSMigratesLegacyBoardPrivately(t *testing.T) {
	skipOnWindows(t)
	upper, lower := filepath.Join(testsupport.TempDir(t), "up"), testsupport.TempDir(t)
	legacy := filepath.Join(lower, "tasks-s1.json")
	if err := os.WriteFile(legacy,
		[]byte(`{"tasks":[{"id":"T1","title":"legacy","status":"pending"}],"next_id":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the permissive legacy mode independent of the test umask — the
	// closing assertion is that the lower copy is left exactly as found, which
	// says nothing under a restrictive umask (a hardened service runs
	// UMask=0077) that already created it 0600.
	if err := os.Chmod(legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	st := NewStore(NewLayeredFS(upper, lower), "agent")
	if err := st.Rebind("s1"); err != nil {
		t.Fatal(err)
	}
	if got := st.List(); len(got) != 1 || got[0].Title != "legacy" {
		t.Fatalf("legacy board did not load: %+v", got)
	}
	if _, err := st.Create([]CreateSpec{{Title: "fresh"}}); err != nil {
		t.Fatal(err)
	}

	assertMode(t, filepath.Join(upper, "tasks-s1.json"), 0o600)
	assertMode(t, upper, 0o700)
	assertMode(t, legacy, 0o644) // lower copy never mutated
}
