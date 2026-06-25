package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/testsupport"
)

func TestRunMigrateCommandDispatch(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantHandled bool
		wantErr     bool
	}{
		{"not ours", []string{"--help"}, false, false},
		{"migrate help", []string{"migrate", "help"}, true, false},
		{"unknown flag errors", []string{"migrate", "--nope"}, true, true},
		{"remove and keep conflict", []string{"migrate", "--remove-old", "--keep-old"}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Even the error paths must never touch real data dirs.
			pinMigrateEnv(t)
			t.Setenv("TERVA_HOME", testsupport.TempDir(t))
			handled, err := runMigrateCommand(tc.args)
			if handled != tc.wantHandled {
				t.Errorf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestRunMigrateEndToEnd(t *testing.T) {
	seed := func(t *testing.T) (base, oldDir, newDir string) {
		t.Helper()
		base = pinMigrateEnv(t)
		oldDir = filepath.Join(base, "zot")
		newDir = filepath.Join(base, "terva")
		writeMigrateFile(t, filepath.Join(oldDir, "config.json"), `{"provider":"anthropic"}`)
		writeMigrateFile(t, filepath.Join(oldDir, "sessions", "x", "s.jsonl"), "line")
		return
	}

	t.Run("yes keep-old", func(t *testing.T) {
		_, oldDir, newDir := seed(t)
		if handled, err := runMigrateCommand([]string{"migrate", "--yes", "--keep-old"}); !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if b, _ := os.ReadFile(filepath.Join(newDir, "config.json")); string(b) != `{"provider":"anthropic"}` {
			t.Errorf("config not copied: %q", b)
		}
		if !envcompat.ZotFallbackDisabled() {
			t.Error("marker not set after a clean migration")
		}
		if _, err := os.Stat(oldDir); err != nil {
			t.Errorf("--keep-old must leave the old dir: %v", err)
		}
	})

	t.Run("yes remove-old", func(t *testing.T) {
		_, oldDir, newDir := seed(t)
		if handled, err := runMigrateCommand([]string{"migrate", "--remove-old"}); !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if _, err := os.Stat(filepath.Join(newDir, "sessions", "x", "s.jsonl")); err != nil {
			t.Errorf("sessions not copied: %v", err)
		}
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Error("--remove-old must delete the old dir")
		}
	})

	t.Run("dry-run changes nothing", func(t *testing.T) {
		_, oldDir, newDir := seed(t)
		if handled, err := runMigrateCommand([]string{"migrate", "--dry-run"}); !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if _, err := os.Stat(filepath.Join(newDir, "config.json")); !os.IsNotExist(err) {
			t.Error("--dry-run must not copy")
		}
		if envcompat.ZotFallbackDisabled() {
			t.Error("--dry-run must not set the marker")
		}
		if _, err := os.Stat(oldDir); err != nil {
			t.Errorf("--dry-run must not remove: %v", err)
		}
	})
}
