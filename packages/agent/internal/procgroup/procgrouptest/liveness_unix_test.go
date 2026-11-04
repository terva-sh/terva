//go:build !windows

package procgrouptest

import (
	"os/exec"
	"testing"
)

// TestGoneReportsAPidReapedDuringTheStateLookup pins the window between the
// kill(2) that finds pid alive and the state lookup that answers next. The
// lookup forks ps(1) on a host without /proc, and that takes about 10ms on
// macOS. A mid-exit child is reaped inside the window, ps then names no
// process, and the lookup reports no zombie. Gone must read that as gone, not
// as running. Before the second kill(2) it reported the dead child as running,
// and the macOS process-group tests failed on a teardown that had worked.
//
// The stand-in here is the timing alone. The process is real, the reap is
// real, and the lookup returns exactly what psZombie returns when ps names no
// process.
func TestGoneReportsAPidReapedDuringTheStateLookup(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid

	t.Cleanup(func() { zombieState = Zombie })
	calls := 0
	zombieState = func(int) bool {
		calls++
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // the reap
		return false   // what psZombie answers when ps names no process
	}

	if !Gone(pid) {
		t.Errorf("Gone(%d) = false for a pid reaped during the state lookup", pid)
	}
	if calls != 1 {
		t.Errorf("the state lookup ran %d times, want 1: Gone did not reach it", calls)
	}
}

// TestGoneReportsARunningPidAsRunning is the positive control. The guard above
// passes for a Gone that always reports gone, and that Gone would make every
// process-group test green over a real orphan leak.
func TestGoneReportsARunningPidAsRunning(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if Gone(cmd.Process.Pid) {
		t.Errorf("Gone(%d) = true for a running process", cmd.Process.Pid)
	}
}
