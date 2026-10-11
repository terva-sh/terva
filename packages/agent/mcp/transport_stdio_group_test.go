//go:build !windows

package mcp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/internal/procgroup/procgrouptest"
	"terva.sh/terva/packages/testsupport"
)

// TestCloseReapsStdioServerProcessGroup is the MCP twin of
// TestStopReapsExtensionChildProcessGroup. `npx -y` and `uvx` run the real
// server as a grandchild that holds the launcher's stdout. Close must reach it
// through the process group, because a kill of the direct child alone leaves
// the grandchild running with nothing left to reap it.
func TestCloseReapsStdioServerProcessGroup(t *testing.T) {
	cases := []struct {
		name string
		// tail runs in the launcher after it starts the grandchild.
		tail string
	}{
		// The launcher ignores stdin EOF, so Close must escalate.
		{"launcher holds out", "exec sleep 300\n"},
		// The launcher exits on stdin EOF, and the grandchild keeps stdout.
		{"launcher exits first", "cat >/dev/null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The background sleep inherits stdout, the pipe terva reads.
			assertCloseReapsChild(t, "sleep 300 &\necho \"$!\" > child.pid\n"+tc.tail)
		})
	}
}

// TestCloseKillsAMemberThatLeftThePipe covers a group member that redirected
// its stdout away from the pipe and ignores SIGTERM. It never holds stdout, so
// stdout reaches EOF and the leader is reaped while the member still runs. A
// Close that took the reaped leader for the end of the server returned there
// and left the member running. Only the group Kill ends it.
func TestCloseKillsAMemberThatLeftThePipe(t *testing.T) {
	cases := []struct {
		name string
		// tail runs in the launcher after it starts the member.
		tail string
	}{
		// The launcher exits on stdin EOF, before any signal.
		{"launcher exits on EOF", "cat >/dev/null\n"},
		// The launcher ignores EOF and exits on the group SIGTERM.
		{"launcher exits on TERM", "exec sleep 300\n"},
	}
	const member = "sh -c 'trap \"\" TERM; exec sleep 300' >/dev/null 2>&1 &\necho \"$!\" > child.pid\n"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertCloseReapsChild(t, member+tc.tail)
		})
	}
}

// assertCloseReapsChild runs script as a stdio server in a fresh directory.
// The script records one child's pid in child.pid. The test closes the
// transport and asserts that the child is gone.
func assertCloseReapsChild(t *testing.T, script string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "server.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tr, err := newStdioTransport(ServerConfig{Command: "/bin/sh", Args: []string{path}}, dir, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(tr.Close) // idempotent; covers an early Fatal

	childPID := waitForChildPID(t, filepath.Join(dir, "child.pid"))
	if procgrouptest.Gone(childPID) {
		t.Fatalf("child pid %d was not running before Close", childPID)
	}

	closed := make(chan struct{})
	go func() { tr.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(stdioCloseGrace + stdioTerminateGrace + 5*time.Second):
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatal("Close did not return")
	}

	// Kill is asynchronous, so poll briefly.
	deadline := time.Now().Add(3 * time.Second)
	for !procgrouptest.Gone(childPID) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(childPID, syscall.SIGKILL) // don't litter the test host
			t.Fatalf("child pid %d survived Close; the group teardown did not reach it", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForChildPID reads the pid the server script recorded for its child.
func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server script never recorded its child's pid")
	return 0
}
