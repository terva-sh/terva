//go:build windows

package privfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 The failure of the public ci on v0.142.0. A reader held a routine's
// record open, the write of its pause was refused, and the routine stayed
// active. The write must now land once the reader is done.
func TestWriteFileWaitsForAReaderOfTheOldFile(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "routine.json")
	if err := WriteFile(path, []byte("active")); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = reader.Close()
		}
	}()

	// The control: this system refuses the rename while the reader is open,
	// with an error that replace reads as busy. Without it the test would
	// pass on a system that has no refusal to wait for.
	other := filepath.Join(filepath.Dir(path), "other.json")
	if err := WriteFile(other, []byte("x")); err != nil {
		t.Fatal(err)
	}
	switch err := os.Rename(other, path); {
	case err == nil:
		t.Skip("this system renames over a file that a reader holds open")
	case !replaceBusy(err):
		t.Fatalf("the rename over an open file failed with %v, which replace does not wait for", err)
	}

	held := 150 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(held)
		_ = reader.Close()
	}()
	start := time.Now()
	err = WriteFile(path, []byte("paused"))
	took := time.Since(start)
	<-done
	closed = true
	if err != nil {
		t.Fatalf("the write failed while a reader held the old file: %v", err)
	}
	if took < held/2 {
		t.Fatalf("the write returned after %s, before the reader was done at %s", took, held)
	}
	if got, _ := os.ReadFile(path); string(got) != "paused" {
		t.Fatalf("the file holds %q, want the new text", got)
	}
}
