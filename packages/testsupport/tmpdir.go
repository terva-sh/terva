// Package testsupport holds small helpers shared across the test suites.
package testsupport

import (
	"os"
	"runtime"
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
	return retryCleanupDir(t, "", "terva-test-")
}

// SocketDir is TempDir for a directory a unix socket will be bound inside.
//
// The kernel caps a unix socket PATH at 104 bytes on darwin and 108 on linux —
// sockaddr_un is a fixed-size struct, so an over-long path is not an error the
// caller sees but a silent truncation, and in practice a bind failure in
// whatever process was handed the path. TempDir cannot be used for this on
// macOS: the default TMPDIR there is a 49-character
// /var/folders/../T path, and t.TempDir() appends the test's own name plus a
// counter, which leaves almost nothing for a filename.
//
// So this asks for /tmp by name. It is the one place a short path matters more
// than a tidy per-test one, and the alternative — every socket-binding test
// hand-rolling os.MkdirTemp and tripping the gate in tempdir_gate_test.go — is
// how the Windows cleanup problem came back the first time.
func SocketDir(t testing.TB) string {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
			base = "/tmp"
		}
	}
	return retryCleanupDir(t, base, "tv")
}

// retryCleanupDir makes a temp dir whose removal rides out a held handle. See
// TempDir for why the retry exists.
func retryCleanupDir(t testing.TB, base, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		t.Fatalf("testsupport: temp dir: %v", err)
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
