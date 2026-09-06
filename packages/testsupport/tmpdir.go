// Package testsupport holds small helpers shared across the test suites.
package testsupport

import (
	"os"
	"testing"
	"time"
)

// TempDir is a drop-in replacement for t.TempDir with a cleanup that tolerates
// a writer still working inside the directory.
//
// Two different races end in the same failed RemoveAll, and the retry below
// rides out both. This used to guard Windows only, on the reasoning that Unix
// has "no such restriction, and nothing to fix". The restriction differs; the
// flake does not.
//
// On Windows a file with an open handle cannot be deleted at all, so cleanup
// fails when a just-written file under the dir (a session .jsonl, a connector
// .log, an extension file) is held for the cleanup instant by a racing async
// close, or by Defender or the search indexer scanning it.
//
// On Unix that same open file deletes fine, and RemoveAll still loses to a
// writer that CREATES. It reads the directory, unlinks what that read saw,
// then rmdirs the parent — and anything appearing in between makes the rmdir
// return ENOTEMPTY. A test that returns while a background goroutine is still
// persisting does exactly this. TestGuidedRetryCarriesGuidanceAndPriorTake
// waits for its turn to START, not to finish, so headless session persistence
// was still writing $TMP/sessions/<id>/ when cleanup ran: about 2 failures in
// 20 runs on Linux, on trunk, blocking pull requests that touched no Go at all.
//
// Retrying covers both, because both windows are brief. If a residual file
// genuinely will not go, this logs rather than fails: a leftover temp dir is
// CI hygiene, not test correctness. A writer that never stops is a leak in the
// code under test rather than a cleanup problem, and that log line is where it
// surfaces.
func TempDir(t testing.TB) string {
	t.Helper()
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
			t.Logf("testsupport.TempDir: residual temp dir left behind (open handle, or a writer that never stopped?): %v", err)
		}
	})
	return dir
}
