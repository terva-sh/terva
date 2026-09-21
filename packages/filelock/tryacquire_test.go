package filelock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/testsupport"
)

// The contract is the same on both platforms, so these tests carry no build
// tag. What differs is only which syscall reports the contention.

func TestTryAcquireTakesAFreeLock(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "free.lock")

	l, held, err := filelock.TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire on a free lock: %v", err)
	}
	if !held {
		t.Fatal("TryAcquire reported a free lock as busy")
	}
	l.Release()
}

func TestTryAcquireReportsBusyRatherThanFailing(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "busy.lock")

	first, err := filelock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 🔑 The error must stay nil. A caller that reads only the error would
	// treat a busy lock as an acquired one, which is the whole failure this
	// three-value signature exists to prevent.
	second, held, err := filelock.TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire on a held lock returned an error rather than busy: %v", err)
	}
	if held {
		second.Release()
		first.Release()
		t.Fatal("TryAcquire took a lock another handle already held")
	}

	first.Release()

	third, held, err := filelock.TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after Release: %v", err)
	}
	if !held {
		t.Fatal("TryAcquire stayed busy after the holder released")
	}
	third.Release()
}

func TestTryAcquireExcludesASecondHandleInThisProcess(t *testing.T) {
	// This is the property every same-process contention test in sessionlock
	// rests on. flock associates a lock with the open file description, and two
	// opens make two descriptions, so the second is denied even though one
	// process owns both. POSIX fcntl record locks do NOT behave this way, which
	// is a reason this package does not use them.
	path := filepath.Join(testsupport.TempDir(t), "twohandles.lock")

	first, held, err := filelock.TryAcquire(path)
	if err != nil || !held {
		t.Fatalf("first TryAcquire: held=%v err=%v", held, err)
	}
	defer first.Release()

	if _, held, err := filelock.TryAcquire(path); err != nil || held {
		t.Fatalf("a second handle in this process took the lock: held=%v err=%v", held, err)
	}
}

func TestReleaseOnANilLockIsSafe(t *testing.T) {
	var l *filelock.Lock
	l.Release() // must not panic; callers defer this beside an error return
}

// TestTryAcquireSeesAnotherProcessHolding proves the exclusion is real across
// processes and not an artifact of one process's bookkeeping, and that killing
// the holder frees it with no timeout of ours.
func TestTryAcquireSeesAnotherProcessHolding(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "crossproc.lock")

	cmd := exec.Command(os.Args[0], "-test.run=TestFilelockHelperProcess")
	cmd.Env = append(os.Environ(), "TERVA_FILELOCK_HELPER=1", "TERVA_FILELOCK_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	// 🚨 The helper parks on a stdin read. Without this pipe exec hands it
	// /dev/null, which reads EOF at once, so the helper returns and its defer
	// releases the lock before the parent ever probes it. The test then passes
	// the parent's acquisition off as a bug in the lock.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a helper process here: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	buf := make([]byte, 5)
	if _, err := stdout.Read(buf); err != nil {
		t.Skipf("the helper did not report holding the lock: %v", err)
	}
	if got := strings.TrimSpace(string(buf)); got != "held" {
		t.Fatalf("helper said %q, want held", got)
	}

	if _, held, err := filelock.TryAcquire(path); err != nil || held {
		t.Fatalf("took a lock a live process holds: held=%v err=%v", held, err)
	}

	// SIGKILL, so the helper runs no cleanup of its own. What frees the lock is
	// the kernel dropping it, which is the property this package is built on.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_, _ = cmd.Process.Wait()

	l, held, err := filelock.TryAcquire(path)
	if err != nil || !held {
		t.Fatalf("a killed holder's lock was not free: held=%v err=%v", held, err)
	}
	l.Release()
}

// TestFilelockHelperProcess is not a test. It is the child half of the test
// above, and it returns immediately unless the parent marked the environment.
func TestFilelockHelperProcess(t *testing.T) {
	if os.Getenv("TERVA_FILELOCK_HELPER") != "1" {
		return
	}
	l, err := filelock.Acquire(os.Getenv("TERVA_FILELOCK_PATH"))
	if err != nil {
		os.Exit(2)
	}
	defer l.Release()
	_, _ = os.Stdout.WriteString("held\n")
	// Block until the parent kills us. Reading a stdin nobody writes to parks
	// the process without a sleep to tune.
	_, _ = os.Stdin.Read(make([]byte, 1))
}
