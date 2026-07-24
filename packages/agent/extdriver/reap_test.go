//go:build !windows

package extdriver

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestReadLoopReapsSelfExitedExtension covers the "unexpected clean exit" leak
// from the zombie-extension forensics note: a successfully handshaken extension
// that exits on its own — before any teardown — must be harvested promptly by
// the read loop, not left as a zombie until session close hours later. reap is
// the sole cmd.Wait owner; the read loop calls it once the stdout stream ends.
func TestReadLoopReapsSelfExitedExtension(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "selfexit")
	hello := `{"type":"hello","name":"selfexit","version":"1.0"}`
	// Announce ready, then block on the host's hello_ack (one '\n'-framed line)
	// so the handshake is fully complete and the read loop is running before we
	// exit 0. Exiting before the ack would trip the hello_ack-write failure path
	// instead of the success-then-exit path this test is about.
	body := "printf '%s\\n' '{\"type\":\"ready\"}'\nread _ack\n"
	writeShellExt(t, dir, hello, body)

	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: "selfexit", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	ext, ok := d.ExtensionByName("selfexit")
	if !ok || ext.cmd == nil {
		t.Fatal("extension not registered with a live process")
	}

	// The process exits on its own; the read loop's deferred reap must harvest
	// it. waitDone closing IS the reap — the close happens-after cmd.Wait
	// returned — so awaiting it (rather than reading ProcessState directly) is
	// race-free under -race.
	select {
	case <-ext.waitDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("self-exited extension pid %d was not reaped within 3s — zombie leak", ext.cmd.Process.Pid)
	}
	if ext.cmd.ProcessState == nil {
		t.Error("ProcessState nil after waitDone closed — Wait did not run")
	}
	if procIsZombie(ext.cmd.Process.Pid) {
		t.Errorf("pid %d is still a zombie after reap", ext.cmd.Process.Pid)
	}

	// Teardown after the process is already reaped must be a no-op, not a
	// double Wait or a hang: the sync.Once in reap guarantees it.
	d.Stop(time.Second)
}

// TestSpawnReapsHandshakeFailureChild covers the handshake-failure leak. A
// wrong-typed first frame is rejected by spawn, and the child — which is still
// running — must be killed AND reaped. This is the note's "untracked live
// process" case (worse than a zombie: it holds real resources), and because
// Load drops the map claim on error, spawn's own error path is the only code
// that can reap it.
func TestSpawnReapsHandshakeFailureChild(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "badhello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Record the pid BEFORE the bad frame (so it is reliably present), send a
	// valid JSON that is not a hello, then stay alive. Staying alive is the
	// point: it forces the reject path to kill a running child, exercising the
	// untracked-live-process fix rather than merely reaping an already-dead one.
	script := "#!/bin/sh\necho $$ > pid\nprintf '%s\\n' '{\"type\":\"nothello\"}'\nsleep 300\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: "badhello", Exec: "./run.sh"}); err == nil {
		d.Stop(time.Second)
		t.Fatal("Load accepted an extension whose first frame was not a hello")
	}

	pidFile := filepath.Join(dir, "pid")
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(pidFile); e == nil {
			if p, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && p > 0 {
				childPID = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("handshake-failure child never recorded its pid")
	}

	// It must end up fully gone (kill -> ESRCH: killed and reaped), never left
	// alive (the leak) and never a lingering zombie (killed but not reaped).
	gone := false
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			gone = true // ESRCH
			break
		}
		if procIsZombie(childPID) {
			t.Fatalf("handshake-failure child pid %d is a zombie — killed but not reaped", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(childPID, syscall.SIGKILL) // don't litter the test host
		t.Fatalf("handshake-failure child pid %d survived Load — neither killed nor reaped (untracked live process)", childPID)
	}
}
