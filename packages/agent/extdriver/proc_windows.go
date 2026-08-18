//go:build windows

package extdriver

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// isolateExtensionProcess puts an extension subprocess in its own process
// group, so the teardown paths below can reach its children.
//
// It used to be a no-op, justified as "there are no POSIX process groups, and
// the kitty-cwd issue it addresses is unix-terminal only". The first clause is
// true and the second describes only ONE of the two reasons. proc_unix.go's
// PAIRING note gives the other: because the extension leads its own group,
// every teardown must signal the GROUP, "otherwise a daemon-style extension's
// background children are orphaned". That reason is not unix-specific, and on
// Windows the children were orphaned.
//
// Windows has its own grouping — CREATE_NEW_PROCESS_GROUP — which is what
// makes CTRL_BREAK_EVENT reach the whole tree below.
func isolateExtensionProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// terminateExtensionGroup asks the extension's whole process group to stop.
//
// CTRL_BREAK_EVENT is the console equivalent of SIGTERM and is delivered to
// every process in the group. p.Signal(syscall.SIGTERM), which this used to
// call, is unimplemented on Windows: it returned an error and did nothing, so
// the "graceful" stage was cosmetic and the force kill was the only thing that
// ever ran.
func terminateExtensionGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}

// killExtensionGroup force-kills the extension.
//
// Same bound as the bash path: this terminates the group LEADER. A child that
// ignores CTRL_BREAK outlives it, and closing that gap needs a Job Object held
// for the process's lifetime. Written down rather than left to be discovered.
func killExtensionGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
