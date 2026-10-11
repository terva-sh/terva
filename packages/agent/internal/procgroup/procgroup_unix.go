//go:build !windows

package procgroup

import (
	"os"
	"os/exec"
	"syscall"
)

// Isolate puts a subprocess in its OWN process group. Call it before
// cmd.Start.
//
// The extension driver adopted it first, for a terminal reason. Some
// terminals (notably kitty) inspect the foreground process group's members to
// decide the cwd for "open a new tab/split here". Extensions run with cmd.Dir
// set to their own install directory, so leaving them in terva's process group
// can make the terminal believe the tab cwd is an extension directory even
// after terva reports the project cwd via OSC 7.
//
// PAIRING: because the subprocess is now its own group leader, every teardown
// path MUST signal the whole GROUP (Terminate / Kill), not just the lead pid —
// otherwise the background children of a daemon-style extension, or the real
// server behind an `npx -y` MCP launcher, are orphaned. See
// TestStopReapsExtensionChildProcessGroup and
// TestCloseReapsStdioServerProcessGroup.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Terminate sends SIGTERM to the whole process group that p leads. The pgid
// equals the leader's pid because Isolate set Setpgid; negating it targets the
// group. A no-op for a nil process.
func Terminate(p *os.Process) {
	signalGroup(p, syscall.SIGTERM)
}

// Kill force-kills (SIGKILL) the whole process group that p leads, reaping any
// background children the subprocess spawned.
func Kill(p *os.Process) {
	signalGroup(p, syscall.SIGKILL)
}

// MembersRemain reports whether the process group that p led still has a
// member terva can signal, after Wait has reaped p. The group outlives its
// leader: a member that redirected its stdout and ignores SIGTERM keeps the
// group after the leader is reaped, and only Kill ends it. (Asked before Wait,
// the leader itself is a member.)
//
// EPERM counts as no member. kill(2) on a group returns EPERM only when no
// member accepts a signal from terva, so Terminate and Kill cannot end the
// group either, and waiting on it buys nothing. EPERM also answers when the
// number now names another user's group, which terva must leave alone.
//
// A zombie member still counts, because kill(2) reaches it. On a host whose
// pid 1 does not reap orphans (a bare container), a caller that waits for the
// group to empty waits out its own deadline instead.
func MembersRemain(p *os.Process) bool {
	if p == nil {
		return false
	}
	return syscall.Kill(-p.Pid, 0) == nil
}

func signalGroup(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	// Negative pid => the process group led by p.Pid (set up by Isolate). If
	// isolation somehow failed, this targets a non-existent group and
	// harmlessly returns ESRCH rather than touching terva's own group.
	_ = syscall.Kill(-p.Pid, sig)
}
