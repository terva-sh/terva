//go:build !windows

package procgrouptest

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Gone reports whether pid is no longer a running process: either fully reaped
// (kill returns ESRCH) or a zombie awaiting reap. A SIGKILL'd orphan reparents
// to pid 1; on a normal host pid 1 (launchd/systemd) reaps it promptly, but in a
// CI container pid 1 is the test runner, which doesn't reap arbitrary orphans —
// so the dead child lingers as a zombie that kill(pid,0) still reports as alive,
// the false "survived" the extension test hit. A zombie is dead-pending-harvest,
// not running, so it counts as gone. This keeps the assertion precise: a
// group-kill MISS leaves the child *sleeping* (state S/R), which Gone still
// rejects, so the orphan regression these tests guard against still fails them.
func Gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true // ESRCH: reaped and gone
	}
	if zombieState(pid) {
		return true
	}
	// The state lookup is not atomic with the kill above. On a host without
	// /proc it forks ps(1), and that takes about 10ms on macOS. A child that
	// was mid-exit at the kill is fully reaped inside the window, and ps then
	// names no process, which is not a zombie. That answer alone calls a dead
	// child alive, the false "still ran when Stop returned" the macOS runner
	// reports. Ask the kernel a second time, which answers in one syscall.
	return syscall.Kill(pid, 0) != nil
}

// zombieState reports whether pid is a zombie. Gone reads the lookup through
// this variable, so a test can stand in for the window the lookup leaves open.
var zombieState = Zombie

// Zombie reports whether pid is a zombie. Linux answers from the state field of
// /proc/<pid>/stat. A host without /proc (macOS, the BSDs) answers from
// `ps -o stat= -p <pid>`. It reports false when neither source names pid.
func Zombie(pid int) bool {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return psZombie(pid)
	}
	return procZombie(pid)
}

// psZombie reads the process state from ps(1). The stat column starts with Z
// for a zombie. A process that has begun to exit carries the E flag before it
// shows Z, and it counts as a zombie here, because a kill(2) on the group had
// already found no member while this helper, reading only Z, called that
// exiting member alive. ps exits non-zero when pid names no process, and a
// reaped process is not a zombie, so that answers false. Gone closes the gap.
func psZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	stat := strings.TrimSpace(string(out))
	return strings.HasPrefix(stat, "Z") || strings.ContainsRune(stat, 'E')
}

func procZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// Format: "pid (comm) state …". comm can contain spaces and parens,
	// so the state char is the first byte after the final ')' + space.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] == 'Z'
}
