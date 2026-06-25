// Package testsupport holds small helpers shared across the test suites.
package testsupport

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TempDir is a drop-in replacement for t.TempDir with a Windows-tolerant
// cleanup.
//
// On Windows a file that still has an open handle cannot be deleted, so
// t.TempDir's automatic RemoveAll can fail — and fail the test — when a
// just-written file under the dir (a session .jsonl, a connector .log, an
// extension file) is held for the cleanup instant: a racing async close
// that's about to complete, or Defender / the search indexer scanning a
// freshly-written file. That timing window is the chronic source of flaky
// Windows CI runs (a different test trips it each run; a re-run passes).
//
// This retries the removal to ride out that brief window, and if a residual
// file genuinely won't go after the retries it logs rather than fails: a
// leftover temp dir is CI hygiene, not test correctness. On non-Windows it
// is exactly t.TempDir (there is no such restriction, and nothing to fix).
func TempDir(t testing.TB) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "terva-test-")
	if err != nil {
		t.Fatalf("testsupport.TempDir: %v", err)
	}
	t.Cleanup(func() {
		for range 20 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		// One last try; on a true never-closed handle this still fails, but a
		// stranded temp dir must not red the run.
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("testsupport.TempDir: residual temp dir left behind (open handle?): %v", err)
		}
	})
	return dir
}
