//go:build !windows

package procgroup

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/internal/procgroup/procgrouptest"
)

// Isolate must put the command in its own process group so the
// group-targeted teardown has a group to signal.
func TestIsolateSetsProcessGroup(t *testing.T) {
	cmd := exec.Command("true")
	Isolate(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("Isolate did not set Setpgid")
	}
}

// MembersRemain must follow the group, not the leader: a member that outlives
// the reaped leader keeps the group alive until Kill ends it.
func TestMembersRemainTracksTheGroupPastItsLeader(t *testing.T) {
	// The leader starts a member, prints its pid and exits at once. The
	// member's output goes nowhere, so Output does not wait on it.
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $!")
	Isolate(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("leader: %v", err)
	}
	member, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		Kill(cmd.Process)
		t.Fatalf("member pid %q: %v", out, err)
	}
	t.Cleanup(func() {
		if !procgrouptest.Gone(member) {
			_ = syscall.Kill(member, syscall.SIGKILL)
		}
	})

	if !MembersRemain(cmd.Process) {
		t.Fatal("MembersRemain reported the group empty while its member still runs")
	}
	Kill(cmd.Process)
	deadline := time.Now().Add(3 * time.Second)
	for MembersRemain(cmd.Process) {
		if time.Now().After(deadline) {
			if procgrouptest.Zombie(member) {
				// Kill worked, but a zombie still counts, and a bare
				// container's pid 1 may never reap it.
				t.Skipf("member %d is a zombie that pid 1 here does not reap", member)
			}
			t.Fatalf("member %d still runs 3s after Kill: Kill did not reach the group", member)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if MembersRemain(nil) {
		t.Error("MembersRemain(nil) = true")
	}
}
