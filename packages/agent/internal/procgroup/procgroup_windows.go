//go:build windows

package procgroup

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Isolate puts a subprocess in its own process group, so the teardown paths
// below can reach its children. Call it before cmd.Start.
//
// It used to be a no-op, justified as "there are no POSIX process groups, and
// the kitty-cwd issue it addresses is unix-terminal only". The first clause is
// true and the second describes only ONE of the two reasons. procgroup_unix.go's
// PAIRING note gives the other: because the subprocess leads its own group,
// every teardown must signal the GROUP, "otherwise a daemon-style extension's
// background children are orphaned". That reason is not unix-specific, and on
// Windows the children were orphaned.
//
// Windows has its own grouping — CREATE_NEW_PROCESS_GROUP — which is what
// makes CTRL_BREAK_EVENT reach the whole tree below.
func Isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// Terminate asks the whole process group that p leads to stop.
//
// CTRL_BREAK_EVENT is the console equivalent of SIGTERM and is delivered to
// every process in the group. p.Signal(syscall.SIGTERM), which the extension
// driver used to call, is unimplemented on Windows: it returned an error and did
// nothing, so the "graceful" stage was cosmetic and the force kill was the only
// thing that ever ran.
func Terminate(p *os.Process) {
	if p == nil {
		return
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}

// MembersRemain reports false: after Wait has reaped p, Windows leaves terva
// nothing it can honestly ask about the group p led.
//
// Windows offers no query for the members of a console process group, and the
// leader is gone by definition once reaped. Reopening it by number would be
// wrong: Wait has closed the handle, so p.Pid can already name an unrelated
// process, and a teardown that believed it would send CTRL_BREAK_EVENT to a
// stale group id. A member that outlives the leader is the gap Kill below
// records.
func MembersRemain(p *os.Process) bool {
	return false
}

// Kill force-kills the subprocess.
//
// Same bound as the bash path: this terminates the group LEADER. A child that
// ignores CTRL_BREAK outlives it, and closing that gap needs a Job Object held
// for the process's lifetime. Written down rather than left to be discovered.
func Kill(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
