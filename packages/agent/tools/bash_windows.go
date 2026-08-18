//go:build windows

package tools

import (
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// setProcessGroup puts the command in its own process group, so
// killProcessGroup can address the whole tree rather than only the shell.
//
// It used to be a no-op, and killProcessGroup called Process.Kill()
// immediately. That lost BOTH halves of the unix behaviour: a command's
// background children were orphaned and kept running after the tool call
// ended, and the process was killed outright with no chance to clean up —
// while the unix side sends SIGTERM, waits three seconds, and only then
// SIGKILLs.
//
// CREATE_NEW_PROCESS_GROUP is what makes the group exist and addressable.
// botcmd_windows.go already uses the same flag for the detached bot.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// killProcessGroup asks the process group to stop, then forces it.
//
// CTRL_BREAK_EVENT is the console equivalent of SIGTERM and is delivered to
// every process in the group, which is what reaches the background children.
// The three-second grace mirrors the unix path exactly.
//
// If the graceful signal cannot be sent — a daemon with no console attached is
// the ordinary case — it kills at once rather than waiting out a grace period
// nothing will use. That keeps this no slower than the immediate kill it
// replaces.
//
// NOT a full tree kill. Process.Kill terminates the group leader only; a child
// that ignores CTRL_BREAK survives it. Doing better needs a Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, whose handle has to be held for the
// process's lifetime — a larger change than restoring the two stages, and one
// nobody here can run. Recorded rather than implied.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		return
	}
	time.AfterFunc(3*time.Second, func() {
		_ = cmd.Process.Kill()
	})
}
