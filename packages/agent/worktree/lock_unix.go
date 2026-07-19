//go:build !windows

package worktree

import (
	"os"
	"syscall"
)

// fileLock is an advisory cross-process lock around a repo-key's registry. Two
// terva instances can target the same repo, so every op takes it. flock is
// released automatically if the holding process dies, so it can't deadlock.
type fileLock struct{ f *os.File }

func acquireLock(path string) (*fileLock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// pidAlive reports whether pid names a live process. signal 0 probes existence:
// nil => alive, EPERM => alive but not ours, ESRCH => gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
