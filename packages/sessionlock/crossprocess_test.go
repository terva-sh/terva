package sessionlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestASessionHeldByAnotherProcessIsRefusedAndAKillFreesIt is the test the
// whole package exists for, and it covers contention and crash recovery in one
// run because they are the same mechanism seen from two sides.
//
// The child is killed with SIGKILL, so it runs no defer and no cleanup. What
// frees the session is the kernel dropping the flock. That is the difference
// between this design and a heartbeat lockfile: there is no staleness timeout
// to wait out, and the assertion below runs immediately after the kill.
func TestASessionHeldByAnotherProcessIsRefusedAndAKillFreesIt(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "20260101-120000-abcd1234.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestSessionLockHelperProcess")
	cmd.Env = append(os.Environ(), "TERVA_SESSIONLOCK_HELPER=1", "TERVA_SESSIONLOCK_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	// The helper parks on a stdin read. Without an explicit pipe exec hands it
	// /dev/null, it reads EOF at once, and its lock is gone before we probe.
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
		t.Skipf("the helper never reported holding the session: %v", err)
	}
	if got := strings.TrimSpace(string(buf)); got != "held" {
		t.Fatalf("helper said %q, want held", got)
	}

	_, err = Acquire(path, Request{Reason: "resume from the TUI", Holder: "terva"})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("a session another process holds must be refused, got %v", err)
	}
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("want a *BusyError, got %T", err)
	}
	if !strings.Contains(err.Error(), "a swarm child is writing") {
		t.Errorf("the refusal must carry the holder's reason: %s", err)
	}
	if busy.Claim.PID != cmd.Process.Pid {
		t.Errorf("the refusal names pid %d, want the helper's %d", busy.Claim.PID, cmd.Process.Pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// No sleep, no retry loop: the flock is already gone.
	h, err := Acquire(path, Request{Reason: "resume from the TUI", Holder: "terva"})
	if err != nil {
		t.Fatalf("a killed holder must not keep the session: %v", err)
	}
	h.Release()
}

// TestSessionLockHelperProcess is not a test. It is the child half of the test
// above and returns at once unless the parent marked the environment.
func TestSessionLockHelperProcess(t *testing.T) {
	if os.Getenv("TERVA_SESSIONLOCK_HELPER") != "1" {
		return
	}
	h, err := Acquire(os.Getenv("TERVA_SESSIONLOCK_PATH"), Request{
		Reason: "a swarm child is writing its reply",
		Holder: "terva swarm",
	})
	if err != nil {
		os.Exit(2)
	}
	defer h.Release()
	_, _ = os.Stdout.WriteString("held\n")
	_, _ = os.Stdin.Read(make([]byte, 1))
}
