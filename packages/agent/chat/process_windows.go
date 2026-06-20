//go:build windows

package chat

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is the Windows STILL_ACTIVE exit code (259): GetExitCodeProcess
// returns it while a process is still running.
const stillActive = 259

// processAlive reports whether a process with the given pid exists. On
// Windows os.Process is always usable and Signal(0) returns nil, so the
// POSIX probe would miss stale pids; query the process exit code instead.
func processAlive(pid int) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// No such process.
			return false, nil
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			// The process exists but we can't inspect it; treat as running.
			return true, nil
		}
		return false, err
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	return code == stillActive, nil
}

func stopProcess(pid int, graceful time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = proc.Signal(os.Interrupt)

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
