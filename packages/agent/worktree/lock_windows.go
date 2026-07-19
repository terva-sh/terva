//go:build windows

package worktree

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileLock is the Windows twin of the unix flock: LockFileEx on the registry
// lockfile. The extension this package was folded in from never ran under
// Windows CI; this port does, so the lock has to be real here too. The OS
// releases the lock if the holding process dies.
type fileLock struct{ f *os.File }

func acquireLock(path string) (*fileLock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
	_ = l.f.Close()
}

// pidAlive reports whether pid names a live process: open a query-only handle
// and check the process hasn't exited. Access denied means it exists but isn't
// ours — alive for our purposes, matching the unix EPERM stance.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true // handle opened: it exists; treat probe failure as alive
	}
	return code == 259 // STILL_ACTIVE
}
