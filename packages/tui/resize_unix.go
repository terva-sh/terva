//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// installResizeHandler subscribes to SIGWINCH and fans each signal out
// to the registered callbacks. OnResizeDetach runs this behind a
// sync.Once, so one channel and one goroutine serve the process however
// many callbacks register. It used to run per registration, which left
// a goroutine and a signal subscription behind on every call.
func (p *ProcTerm) installResizeHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			p.fireResize()
		}
	}()
}

func (p *ProcTerm) SetNonblock(enable bool) error {
	return syscall.SetNonblock(int(p.in.Fd()), enable)
}

// peekStdin polls the stdin fd for up to d; if a byte is ready, reads
// and returns it. Returns (0, false, nil) on timeout.
//
// Uses golang.org/x/sys/unix so the syscall signatures line up on both
// linux (syscall.Select returns (int, error), Timeval.Usec is int64)
// and darwin (Select returns error only, Timeval.Usec is int32) —
// pulling this through the x/sys wrappers makes both builds happy.
func peekStdin(in *os.File, d time.Duration) (byte, bool, error) {
	fd := int(in.Fd())
	var rset unix.FdSet
	rset.Set(fd)
	tv := unix.NsecToTimeval(int64(d))
	_, err := unix.Select(fd+1, &rset, nil, nil, &tv)
	if err != nil {
		if err == unix.EINTR {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !rset.IsSet(fd) {
		return 0, false, nil
	}
	var b [1]byte
	_, rerr := in.Read(b[:])
	if rerr != nil {
		return 0, false, rerr
	}
	return b[0], true, nil
}
