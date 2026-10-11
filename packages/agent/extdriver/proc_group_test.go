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

	"terva.sh/terva/packages/agent/internal/procgroup"
	"terva.sh/terva/packages/agent/internal/procgroup/procgrouptest"
	"terva.sh/terva/packages/testsupport"
)

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

	childPID := waitForExtChildPID(t, filepath.Join(dir, "child.pid"), func() { d.Stop(time.Second) })
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child pid %d not alive before Stop: %v", childPID, err)
	}

	// Short grace so the holdout escalates to the group SIGTERM/SIGKILL.
	d.Stop(300 * time.Millisecond)

	// The child must be killed, not left running. Poll briefly — the
	// SIGKILL escalation is async (1s after SIGTERM in stopExtensions).
	awaitExtChildGone(t, childPID, "group-kill did not reap it (orphaned)")
}

// TestStopEndsTheGroupOfAnExtensionThatExitsCleanly covers a well-behaved
// extension with a child. The extension exits on the shutdown frame, inside the
// grace. A stop that took the reaped leader for the end of the extension
// returned there and left the child running. The child must be gone too.
func TestStopEndsTheGroupOfAnExtensionThatExitsCleanly(t *testing.T) {
	cases := []struct {
		name string
		// child starts one background child. The script records its pid.
		child string
		// sigkill marks a child that only the group Kill ends. Stop does not
		// wait for a killed process to finish dying, so the test polls for
		// that one. Every other child must be gone when Stop returns.
		sigkill bool
	}{
		// The child holds stdout, so the read loop cannot reach EOF and the
		// leader stays unreaped until the group is stopped.
		{name: "child holds stdout", child: "sleep 300 &"},
		// The child redirected its stdout. The leader is reaped as soon as it
		// exits, while the child still runs.
		{name: "child left the pipe", child: "sleep 300 >/dev/null 2>&1 &"},
		// The same, and the child ignores SIGTERM.
		{
			name:    "child left the pipe and ignores TERM",
			child:   `sh -c 'trap "" TERM; exec sleep 300' >/dev/null 2>&1 &`,
			sigkill: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := testsupport.TempDir(t)
			dir := filepath.Join(tmp, "tidy")
			body := `printf '%s\n' '{"type":"ready"}'
` + tc.child + `
echo "$!" > child.pid
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
			writeShellExt(t, dir, `{"type":"hello","name":"tidy","version":"1.0"}`, body)
			d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
			if err := d.Load(context.Background(), dir, Manifest{Name: "tidy", Exec: "./run.sh"}); err != nil {
				t.Fatalf("load: %v", err)
			}
			ext, ok := d.ExtensionByName("tidy")
			if !ok || ext.cmd == nil {
				t.Fatal("extension not registered with a live process")
			}
			childPID := waitForExtChildPID(t, filepath.Join(dir, "child.pid"), func() { d.Stop(time.Second) })
			if procgrouptest.Gone(childPID) {
				t.Fatalf("child pid %d was not running before Stop", childPID)
			}

			const grace = 300 * time.Millisecond
			stopped := make(chan struct{})
			go func() { d.Stop(grace); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(grace + terminateGrace + 5*time.Second):
				_ = syscall.Kill(childPID, syscall.SIGKILL)
				t.Fatal("Stop did not return")
			}
			// A zero exit status means the shutdown frame reached the
			// extension before any signal did.
			if ext.waitErr != nil {
				t.Errorf("extension did not exit cleanly on shutdown: %v", ext.waitErr)
			}
			if !tc.sigkill {
				if !procgrouptest.Gone(childPID) {
					_ = syscall.Kill(childPID, syscall.SIGKILL)
					t.Fatalf("child pid %d still ran when Stop returned", childPID)
				}
				return
			}
			awaitExtChildGone(t, childPID, "the group Kill did not reach it")
		})
	}
}

// TestSelfExitEndsTheExtensionGroup covers an extension that exits on its own
// and leaves a child behind. The extension stays in the loaded set, and no stop
// may come for hours. By then the group id can name another group, so the read
// loop ends the group straight after it reaps the leader.
func TestSelfExitEndsTheExtensionGroup(t *testing.T) {
	t.Parallel()
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "quitter")
	// Wait for hello_ack, so the read loop runs before the exit.
	body := `printf '%s\n' '{"type":"ready"}'
sleep 300 >/dev/null 2>&1 &
echo "$!" > child.pid
read _ack
exit 0
`
	writeShellExt(t, dir, `{"type":"hello","name":"quitter","version":"1.0"}`, body)
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: "quitter", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { d.Stop(time.Second) })
	childPID := waitForExtChildPID(t, filepath.Join(dir, "child.pid"), nil)
	awaitExtChildGone(t, childPID, "the read loop did not end the group of a self-exited extension")
}

// TestStopAfterTheGroupEndedSignalsNothing covers a stop that arrives after the
// teardown is over. By then the extension's group id is free, and another group
// can hold the same number. procgroup.Stop asks by number, so it would report
// that group as a member left and signal it. A live decoy group stands in for
// the reused number here.
func TestStopAfterTheGroupEndedSignalsNothing(t *testing.T) {
	cases := []struct {
		name string
		body string
		// end brings the teardown to its end.
		end func(d *Driver, ext *Extension)
	}{
		{
			name: "after a stop",
			body: `printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`,
			end: func(_ *Driver, ext *Extension) { stopExtensions([]*Extension{ext}, time.Second) },
		},
		{
			name: "after a self exit",
			body: `printf '%s\n' '{"type":"ready"}'
read _ack
exit 0
`,
			// endGroup returns once the one teardown, the read loop's or
			// this call's, is over.
			end: func(_ *Driver, ext *Extension) { <-ext.waitDone; ext.endGroup(0) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := testsupport.TempDir(t)
			dir := filepath.Join(tmp, "ended")
			writeShellExt(t, dir, `{"type":"hello","name":"ended","version":"1.0"}`, tc.body)
			d := New(tmp, "", "0.0.0-test", "anthropic", "opus", &stubHooks{})
			if err := d.Load(context.Background(), dir, Manifest{Name: "ended", Exec: "./run.sh"}); err != nil {
				t.Fatalf("load: %v", err)
			}
			ext, ok := d.ExtensionByName("ended")
			if !ok || ext.cmd == nil {
				t.Fatal("extension not registered with a live process")
			}
			tc.end(d, ext)

			decoy := exec.Command("sleep", "300")
			procgroup.Isolate(decoy)
			if err := decoy.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { _ = decoy.Wait(); close(exited) }()
			t.Cleanup(func() { _ = decoy.Process.Kill(); <-exited })
			if !procgroup.MembersRemain(decoy.Process) {
				t.Fatal("the decoy does not lead a live group, so this test proves nothing")
			}

			// The decoy's group now holds the number the extension's group had.
			ext.cmd.Process.Pid = decoy.Process.Pid
			stopExtensions([]*Extension{ext}, 100*time.Millisecond)
			select {
			case <-exited:
				t.Fatal("a stop after the teardown signalled the group that reused the extension's group id")
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}

// waitForExtChildPID reads the pid an extension script recorded for its child.
// onFail runs before the test fails, so a caller can stop the driver.
func waitForExtChildPID(t *testing.T, path string, onFail func()) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if onFail != nil {
		onFail()
	}
	t.Fatal("extension child never recorded its pid")
	return 0
}

// awaitExtChildGone polls briefly, because a Kill is asynchronous. It kills a
// survivor before it fails, so a failed run leaves nothing on the host.
func awaitExtChildGone(t *testing.T, pid int, why string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !procgrouptest.Gone(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL) // don't litter the test host
			t.Fatalf("extension child pid %d survived: %s", pid, why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
