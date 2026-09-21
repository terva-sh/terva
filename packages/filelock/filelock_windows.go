//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Lock is the Windows twin of the unix flock: LockFileEx on the lockfile. The
// OS releases the lock if the holding process dies.
type Lock struct{ f *os.File }

// The guard is one byte at 2^62, and the offset is the whole point.
//
// flock is advisory and whole-file: it answers "does anyone hold this" and
// never stands between the holder and its own writes. LockFileEx is neither.
// It locks a BYTE RANGE, and the range is MANDATORY — the kernel enforces it
// against every other handle on the file, including handles the locking
// process itself owns.
//
// So a guard on [0,1) of a session transcript denies the transcript's own
// writer, which holds a separate handle, the moment it writes the first record
// at offset 0. That is not a lock doing its job; it is a lock the design never
// meant to take, because sessionlock.lockTranscript reasons in flock's terms
// and takes its guard on the transcript itself rather than a file beside it.
// Every session write on Windows failed this way.
//
// 2^62 is past any real transcript, so no data write ever reaches the locked
// byte and the mandatory range costs nothing. Exclusion is unaffected: every
// process locks the identical range on the identical file. Locking beyond EOF
// is legal and does not extend the file.
//
// Lock and unlock must name the same range, so all three call sites take it
// from here rather than spelling out an offset each.
const (
	lockOffsetLow  = 0
	lockOffsetHigh = 0x40000000 // 2^62 once combined with the low word
	lockBytes      = 1
)

// lockRegion is a fresh Overlapped naming the guard byte. LockFileEx reads the
// offset from the Overlapped and the length from its own arguments, and it
// writes to the struct, so each call gets its own.
func lockRegion() *windows.Overlapped {
	return &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
}

// Acquire blocks until the lock at path is held.
func Acquire(path string) (*Lock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	ol := lockRegion()
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lockBytes, 0, ol); err != nil {
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
// LOCKFILE_FAIL_IMMEDIATELY turns the wait into ERROR_LOCK_VIOLATION.
// ERROR_IO_PENDING answers the same question here: the lock is not ours now.
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
	ol := lockRegion()
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockBytes, 0, ol)
	if err != nil {
		_ = f.Close()
		if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &Lock{f: f}, true, nil
}

// Release drops the lock. Safe on a nil Lock, so a caller can defer it
// alongside an error return.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	ol := lockRegion()
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, lockBytes, 0, ol)
	_ = l.f.Close()
}

// PIDAlive reports whether pid names a live process: open a query-only handle
// and check the process hasn't exited. Access denied means it exists but isn't
// ours — alive for our purposes, matching the unix EPERM stance.
func PIDAlive(pid int) bool {
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
