//go:build !windows

package external

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

// TestStopReapsConnectorProcessGroup is the connector twin of
// TestCloseReapsStdioServerProcessGroup. A launcher (`npx`, `uv run`, a shell
// script) runs the real connector as its own child, and a connector can start
// children of its own. Stopping the connector must reach all of them through
// the process group. A signal to the direct child alone left them running.
func TestStopReapsConnectorProcessGroup(t *testing.T) {
	cases := []struct {
		name string
		// script is the launcher. It starts one child, records the child's
		// pid in child.pid, and then runs $CONNECTOR, the helper connector.
		script string
		// sigkill marks a child that only the group Kill ends. Receive does
		// not wait for a killed process to finish dying, so the test polls
		// for that one. Every other child must be gone when Receive returns.
		sigkill bool
	}{
		// The launcher ignores stdin EOF after the connector exits, so the
		// stop must escalate. The child holds stdout.
		{name: "launcher holds out", script: "sleep 300 &\necho \"$!\" > child.pid\n$CONNECTOR\nexec sleep 300\n"},
		// The connector exits on shutdown, and the child keeps stdout. The
		// session cannot end, so the leader stays unreaped until the group
		// is stopped.
		{name: "connector exits first", script: "sleep 300 &\necho \"$!\" > child.pid\nexec $CONNECTOR\n"},
		// The child redirected its stdout and ignores SIGTERM. The leader is
		// reaped at once, and only the group Kill ends the child.
		{
			name:    "child left the pipe",
			script:  "sh -c 'trap \"\" TERM; exec sleep 300' >/dev/null 2>&1 &\necho \"$!\" > child.pid\nexec $CONNECTOR\n",
			sigkill: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newLauncherProxy(t, tc.script, "happy")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := p.Connect(ctx); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			r := startReceive(ctx, p)
			pid := waitForChildPIDs(t, filepath.Join(dir, "child.pid"), 1)[0]
			if procgrouptest.Gone(pid) {
				t.Fatalf("child pid %d was not running before the stop", pid)
			}

			cancel()
			_ = r.end(t)
			if !tc.sigkill {
				if !procgrouptest.Gone(pid) {
					t.Fatalf("child pid %d still ran when Receive returned", pid)
				}
				return
			}
			awaitPIDGone(t, pid, "the group Kill did not reach it")
		})
	}
}

// TestRestartStopsTheCrashedGroupFirst covers the restart loop. A connector
// that crashes can leave a child behind. The proxy must stop that child before
// it starts the replacement, or a crash loop piles up old processes, each one
// still holding the chat service's session.
func TestRestartStopsTheCrashedGroupFirst(t *testing.T) {
	t.Parallel()
	// The child redirected its stdout, so the crash ends the session at once,
	// and the leader is reaped while the child still runs.
	script := "sleep 300 >/dev/null 2>&1 &\necho \"$!\" >> child.pid\nexec $CONNECTOR\n"
	p, dir := newLauncherProxy(t, script, "crash-once")
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := startReceive(ctx, p)
	if m := r.next(t); m.Text != "first-life" {
		t.Fatalf("first message = %+v", m)
	}
	first := waitForChildPIDs(t, pidFile, 1)[0]

	if m := r.next(t); m.Text != "second-life" {
		t.Fatalf("post-restart message = %+v", m)
	}
	if !procgrouptest.Gone(first) {
		t.Fatalf("child pid %d of the crashed connector still ran beside its replacement", first)
	}
	second := waitForChildPIDs(t, pidFile, 2)[1]

	cancel()
	_ = r.end(t)
	if !procgrouptest.Gone(second) {
		t.Fatalf("child pid %d of the replacement still ran when Receive returned", second)
	}
}

// TestAbortAfterStopSignalsNothing covers an abort that arrives after the
// teardown finished. By then the connector's group id is free, and another
// group can hold the same number. procgroup.Gone asks by number, so it would
// report that group as a member left, and kill would end it. A live decoy
// group stands in for the reused number here.
func TestAbortAfterStopSignalsNothing(t *testing.T) {
	t.Parallel()
	p, _, _ := newTestProxy(t, "happy")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	p.mu.Lock()
	c := p.child
	p.mu.Unlock()
	p.stopAll() // returns once every goroutine that touches c is done

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

	// The decoy's group now holds the number the connector's group had.
	c.cmd.Process.Pid = decoy.Process.Pid
	c.kill()
	select {
	case <-exited:
		t.Fatal("a kill after the teardown ended the group that reused the connector's group id")
	case <-time.After(300 * time.Millisecond):
	}
}

// newLauncherProxy builds a proxy whose connector is script, run by /bin/sh in
// a fresh manifest directory. The script names the helper connector as
// $CONNECTOR, which runs TestHelperConnector in mode. The stop graces are
// short, because no case here ever exits within them. At cleanup it kills
// every child the launcher recorded, so a failed run leaves none on the host.
func newLauncherProxy(t *testing.T, script, mode string) (*Proxy, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// "$@" passes on the run verb that the proxy appends, the way a real
	// launcher passes its arguments to the program it starts.
	connector := shQuote(exe) + " '-test.run=^TestHelperConnector$' " + mode + ` "$@"`
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "launcher.sh")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(script, "$CONNECTOR", connector)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, pid := range recordedPIDs(filepath.Join(dir, "child.pid")) {
			if !procgrouptest.Gone(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	m := Manifest{Name: "fake", Version: "9.9.9", Exec: "/bin/sh", Args: []string{path}}
	p := NewProxy(m, dir, testsupport.TempDir(t), (&warnLog{}).add)
	p.restartDelay = 5 * time.Millisecond
	p.stopGrace = 300 * time.Millisecond
	p.terminateGrace = 300 * time.Millisecond
	return p, dir
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// waitForChildPIDs waits until the launcher recorded at least n child pids.
func waitForChildPIDs(t *testing.T, path string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pids := recordedPIDs(path); len(pids) >= n {
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the launcher recorded fewer than %d child pids", n)
	return nil
}

// recordedPIDs reads the pids the launcher recorded, one per line.
func recordedPIDs(path string) []int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if p, err := strconv.Atoi(f); err == nil && p > 0 {
			pids = append(pids, p)
		}
	}
	return pids
}

// awaitPIDGone polls briefly, because a Kill is asynchronous.
func awaitPIDGone(t *testing.T, pid int, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !procgrouptest.Gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child pid %d survived the stop: %s", pid, why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
