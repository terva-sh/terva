//go:build windows

package extdriver

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateExtensionProcess is a no-op on Windows: there are no POSIX
// process groups, and the kitty-cwd issue it addresses is unix-terminal
// only.
func isolateExtensionProcess(_ *exec.Cmd) {}

// terminateExtensionGroup best-effort signals the process. Windows has no
// process-group kill; SIGTERM is unsupported there (a no-op), matching the
// prior single-process teardown — the force kill below is what actually
// stops a holdout.
func terminateExtensionGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Signal(syscall.SIGTERM)
}

// killExtensionGroup force-kills the process.
func killExtensionGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
