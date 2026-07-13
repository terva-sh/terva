package tasks

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// layeredFS is a test double mirroring the terva SDK's ext.DataFS read-through
// semantics: reads prefer the writable upper dir and fall through to the
// read-only lower dir; writes always land in upper (copy-on-write). It lets us
// verify, without importing the SDK, that the store migrates a legacy board
// (written under the old DataDir, now the install/lower layer) forward.
type layeredFS struct{ upper, lower string }

func (f layeredFS) ReadFile(name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(f.upper, name))
	if err == nil {
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return os.ReadFile(filepath.Join(f.lower, name))
}

func (f layeredFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	p := filepath.Join(f.upper, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, perm)
}

func (f layeredFS) Path(name string) (string, error) {
	return filepath.Join(f.upper, name), nil
}

// TestMigratesLegacyBoardForward: a board written under the old DataDir (now the
// read-only lower layer) loads via fall-through, and the next mutation rewrites
// it into the new writable upper layer — the v0.105.2 data-dir split, end to end.
func TestMigratesLegacyBoardForward(t *testing.T) {
	upper := testsupport.TempDir(t)
	lower := testsupport.TempDir(t)
	fs := layeredFS{upper: upper, lower: lower}

	// A legacy board sits only in the lower (old install-dir) layer.
	legacy := `{"tasks":[{"id":"task-1","title":"legacy task","status":"pending",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],"next_id":2}`
	if err := os.WriteFile(filepath.Join(lower, "tasks-sess.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewStore(fs, "a")
	s.now = fixedClock()
	if err := s.Rebind("sess"); err != nil {
		t.Fatal(err)
	}

	// The legacy task loaded through the fall-through, before any write.
	got := s.List()
	if len(got) != 1 || got[0].Title != "legacy task" {
		t.Fatalf("legacy board not loaded via fall-through: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(upper, "tasks-sess.json")); !os.IsNotExist(err) {
		t.Errorf("nothing should be written to the upper layer before a mutation")
	}

	// A mutation migrates the board into the writable upper layer; nextID from the
	// legacy file is honored (new task is task-2).
	created, err := s.Create([]CreateSpec{{Title: "new task"}})
	if err != nil {
		t.Fatal(err)
	}
	if created[0].ID != "task-2" {
		t.Errorf("legacy next_id not honored across migration: %q", created[0].ID)
	}
	if _, err := os.Stat(filepath.Join(upper, "tasks-sess.json")); err != nil {
		t.Errorf("migrated board should be written to the upper (data) layer: %v", err)
	}

	// The lower (read-only install) copy is never modified.
	lo, err := os.ReadFile(filepath.Join(lower, "tasks-sess.json"))
	if err != nil || string(lo) != legacy {
		t.Errorf("lower-layer legacy file must be left untouched")
	}

	// A fresh store over the same layers now reads the migrated upper copy and
	// sees both tasks.
	s2 := NewStore(fs, "a")
	s2.now = fixedClock()
	if err := s2.Rebind("sess"); err != nil {
		t.Fatal(err)
	}
	if n := len(s2.List()); n != 2 {
		t.Errorf("reload after migration should see both tasks, got %d", n)
	}
}
