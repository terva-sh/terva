//go:build !windows

package chat

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// processAlive reports whether a process with the given pid exists.
// signal 0 is POSIX's "does the process exist?" probe.
func processAlive(pid int) (bool, error) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		// Other errors (EPERM) mean the process exists but we can't inspect it.
		return true, nil
	}
	return true, nil
}

func stopProcess(pid int, graceful time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = proc.Signal(syscall.SIGTERM)

	deadline := time.Now().Add(graceful)
	for time.Now().Before(deadline) {
		alive, err := processAlive(pid)
		if err != nil || !alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = proc.Kill()
	return nil
}
