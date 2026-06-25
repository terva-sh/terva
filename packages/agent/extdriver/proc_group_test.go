//go:build !windows

package extdriver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// isolateExtensionProcess must put the command in its own process group so
// the group-targeted teardown has a group to signal.
func TestIsolateExtensionProcessSetsProcessGroup(t *testing.T) {
	cmd := exec.Command("true")
	isolateExtensionProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("isolateExtensionProcess did not set Setpgid")
	}
}

// TestStopReapsExtensionChildProcessGroup is the safety net for the
// Setpgid/group-kill pairing: a daemon-style extension that spawns a
// background child and then ignores shutdown (a holdout) must have its
// WHOLE process group reaped on Stop. Without the group-kill, the child is
// reparented to init and survives — the orphan regression Setpgid would
// otherwise introduce.
func TestStopReapsExtensionChildProcessGroup(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "daemon")
	hello := `{"type":"hello","name":"daemon","version":"1.0"}`
	// Announce ready, spawn a background child recording its pid into the
	// install dir (the script's cwd), then exec a long sleep so the
	// extension ignores shutdown and forces teardown to the group signal.
	body := `printf '%s\n' '{"type":"ready"}'
sleep 300 &
echo "$!" > child.pid
exec sleep 300
`
	writeShellExt(t, dir, hello, body)

	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: "daemon", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Wait for the child to spawn and record its pid.
	pidFile := filepath.Join(dir, "child.pid")
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				childPID = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		d.Stop(time.Second)
		t.Fatal("extension child never recorded its pid")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child pid %d not alive before Stop: %v", childPID, err)
	}

	// Short grace so the holdout escalates to the group SIGTERM/SIGKILL.
	d.Stop(300 * time.Millisecond)

	// The child must be reaped, not orphaned. Poll briefly — the SIGKILL
	// escalation is async (1s after SIGTERM in stopExtensions).
	gone := false
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			gone = true // ESRCH: the process no longer exists
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(childPID, syscall.SIGKILL) // don't litter the test host
		t.Fatalf("extension child pid %d survived Stop — group-kill did not reap it (orphaned)", childPID)
	}
}
