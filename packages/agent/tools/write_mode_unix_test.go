//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A mode is honored exactly, defeating the umask — the whole point, since under
// a 0077 umask a plain write can never be executable. Forcing a restrictive
// umask is what makes this test guard the Chmod: without it, os.WriteFile would
// already produce 0755 and the Chmod would be untested.
func TestWriteModeCreatesExecutable(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := testsupport.TempDir(t)
	tool := &WriteTool{CWD: dir}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "setup.sh", "content": "#!/bin/sh\necho hi\n", "mode": "0755",
	}), nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %#o, want 0755 (Chmod must pin the mode past the umask)", got)
	}
}

// Omitting mode must never change an existing file's mode — a write can't
// silently broaden a 0600 secret.
func TestWriteModeOmittedPreservesExisting(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "secret")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &WriteTool{CWD: dir}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "secret", "content": "new",
	}), nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %#o, want 0600 preserved — a write with no mode must not broaden it", got)
	}
}
