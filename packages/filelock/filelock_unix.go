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

// TryAcquire is Acquire that reports contention instead of waiting for it.
//
// Three outcomes, and a caller must tell the last two apart: (lock, true, nil)
// holds it, (nil, false, nil) means somebody else does, and (nil, false, err)
// means the probe itself failed. A caller that reads only the error treats a
// busy lock as success.
//
// EINTR is not contention. A signal can interrupt the syscall before the kernel
// decides anything, so the loop asks again rather than report a free lock as
// taken.
func TryAcquire(path string) (*Lock, bool, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, false, err
	}
	return TryLockFile(f)
}

// TryLockFile is TryAcquire against a file the caller already opened, for a
// caller that must decide what to open and with which flags — the session lock
// takes its guard on the transcript itself, which must never be created by the
// act of probing it.
//
// It takes ownership of f: the returned Lock closes it on Release, and f is
// closed before any non-nil error or a busy result is returned.
func TryLockFile(f *os.File) (*Lock, bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, true, nil
		}
		if err == syscall.EINTR {
			continue
		}
		_ = f.Close()
		// EAGAIN and EWOULDBLOCK are one value on Linux and separate values on
		// some other unixes, so both are named.
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, false, nil
		}
		return nil, false, err
	}
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
