//go:build !windows

package extdriver

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateExtensionProcess puts an extension subprocess in its OWN process
// group. Some terminals (notably kitty) inspect the foreground process
// group's members to decide the cwd for "open a new tab/split here";
// extensions run with cmd.Dir set to their own install directory, so
// leaving them in terva's process group can make the terminal believe the
// tab cwd is an extension directory even after terva reports the project
// cwd via OSC 7.
//
// PAIRING: because the extension is now its own group leader, every
// teardown path MUST signal the whole GROUP (terminateExtensionGroup /
// killExtensionGroup), not just the lead pid — otherwise a daemon-style
// extension's background children are orphaned. See
// TestStopReapsExtensionChildProcessGroup.
func isolateExtensionProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateExtensionGroup sends SIGTERM to the extension's whole process
// group. The pgid equals the leader's pid because spawn set Setpgid;
// negating it targets the group. A no-op for a nil process.
func terminateExtensionGroup(p *os.Process) {
	signalExtensionGroup(p, syscall.SIGTERM)
}

// killExtensionGroup force-kills (SIGKILL) the extension's whole process
// group, reaping any background children a daemon-style extension spawned.
func killExtensionGroup(p *os.Process) {
	signalExtensionGroup(p, syscall.SIGKILL)
}

func signalExtensionGroup(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	// Negative pid => the process group led by p.Pid (set up by
	// isolateExtensionProcess). If isolation somehow failed, this targets a
	// non-existent group and harmlessly returns ESRCH rather than touching
	// terva's own group.
	_ = syscall.Kill(-p.Pid, sig)
}
