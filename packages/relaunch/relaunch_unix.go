//go:build unix

package relaunch

import "syscall"

// supported: exec(2) replaces the process image on unix.
const supported = true

// realExec replaces the current process with the launch binary, same argv/env.
// On success it never returns (the image is gone); it returns only on error.
func realExec() error {
	return syscall.Exec(launchExe, launchArgs, launchEnv)
}
