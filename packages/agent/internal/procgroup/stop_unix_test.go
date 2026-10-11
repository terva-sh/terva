//go:build !windows

package procgroup

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/internal/procgroup/procgrouptest"
)

// startGroup runs script as an isolated group leader. The script prints the
// pid of one member first. A goroutine owns Wait and closes the returned
// channel when it returns, the way Stop's callers do.
func startGroup(t *testing.T, script string) (*exec.Cmd, int, chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	Isolate(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	member, aerr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || aerr != nil {
		Kill(cmd.Process)
		_ = cmd.Wait()
		t.Fatalf("member pid %q: %v %v", line, err, aerr)
	}
	t.Cleanup(func() {
		if !procgrouptest.Gone(member) {
			_ = syscall.Kill(member, syscall.SIGKILL)
		}
	})
	go func() { _ = cmd.Wait(); close(reaped) }()
	return cmd, member, reaped
}

// Stop must reach a member that ignores SIGTERM with the group Kill, even
// when the leader itself ends on the Terminate.
func TestStopKillsAMemberThatIgnoresTerminate(t *testing.T) {
	cmd, member, reaped := startGroup(t,
		"sh -c 'trap \"\" TERM; exec sleep 300' >/dev/null 2>&1 & echo $!; exec sleep 300")
	released := false
	Stop(cmd.Process, reaped, 100*time.Millisecond, 100*time.Millisecond, func() { released = true })
	if !released {
		t.Error("Stop returned before the Kill, with a member that ignores SIGTERM still in the group")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !procgrouptest.Gone(member) {
		if time.Now().After(deadline) {
			t.Fatalf("member %d still runs after Stop: the Kill did not reach the group", member)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Stop must wait for a member that outlives the leader, and return without a
// signal once that member exits on its own, inside the first grace.
func TestStopReturnsOnceTheGroupIsGone(t *testing.T) {
	const grace = 3 * time.Second
	cmd, member, reaped := startGroup(t, "sleep 0.3 >/dev/null 2>&1 & echo $!; exit 0")
	start := time.Now()
	killed := false
	Stop(cmd.Process, reaped, grace, time.Second, func() { killed = true })
	took := time.Since(start)
	if killed && procgrouptest.Zombie(member) {
		// A zombie still counts as a member, and a bare container's pid 1
		// may never reap it.
		t.Skipf("member %d is a zombie that pid 1 here does not reap", member)
	}
	if killed || took >= grace {
		t.Fatalf("Stop took %s and killed=%v for a group that emptied by itself", took, killed)
	}
	if !procgrouptest.Gone(member) {
		t.Fatalf("Stop returned while member %d still ran", member)
	}
	if !Gone(cmd.Process, reaped) {
		t.Error("Gone reports the group alive after Stop returned")
	}
}
