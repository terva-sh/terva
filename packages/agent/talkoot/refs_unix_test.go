//go:build unix

package talkoot

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 🚨 A named pipe blocks an open for reading until a writer comes. A member
// can make one and cite it, so the read must refuse it at once.
func TestReadRefRefusesANamedPipeWithoutWaiting(t *testing.T) {
	dir, home := refFixture(t)
	if err := syscall.Mkfifo(filepath.Join(home, "docs", "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadRef(dir, home, "path:docs/pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a named pipe was read as a file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a named pipe blocked")
	}
}

// A note is refused before its open when it is not a regular file, so a pipe
// in the notes directory cannot hang the read either.
func TestReadRefRefusesANamedPipeNote(t *testing.T) {
	dir, home := refFixture(t)
	if _, err := WriteNote(dir, "helm", "real.md", "x"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, NotesDir, "helm", "pipe.md"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadRef(dir, home, "note:helm/pipe.md")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a named pipe was read as a note")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a named pipe note blocked")
	}
}
