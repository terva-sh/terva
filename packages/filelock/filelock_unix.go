//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// Lock is a held advisory cross-process lock. flock is released automatically
// if the holding process dies, so it cannot deadlock.
type Lock struct{ f *os.File }

// Acquire blocks until the lock at path is held.
func Acquire(path string) (*Lock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe on a nil Lock, so a caller can defer it
// alongside an error return.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// PIDAlive reports whether pid names a live process. Signal 0 probes
// existence: nil => alive, EPERM => alive but not ours, ESRCH => gone.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
