//go:build !windows

package tui

// The real SIGWINCH path, end to end.
//
// resize_test.go drives fireResize directly, which proves the registry
// logic but not the signal plumbing. This one sends the process a real
// SIGWINCH and counts invocations, so it fails if installResizeHandler
// ever subscribes more than once again: N subscriptions mean the signal
// is delivered N times, and each delivery walks every callback.
//
// Windows has no SIGWINCH and resize_windows.go leaves
// installResizeHandler empty, so this is Unix only.

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSIGWINCHFiresEachCallbackExactlyOnce(t *testing.T) {
	p := NewProcTerm()

	const callbacks = 4
	var counts [callbacks]atomic.Int32
	fired := make(chan struct{}, callbacks)
	for i := 0; i < callbacks; i++ {
		i := i
		p.OnResize(func() {
			counts[i].Add(1)
			select {
			case fired <- struct{}{}:
			default:
			}
		})
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("send SIGWINCH: %v", err)
	}

	// Wait for the fan-out to reach every callback once.
	for i := 0; i < callbacks; i++ {
		select {
		case <-fired:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d callbacks fired within 5s", i, callbacks)
		}
	}

	// Then let any duplicate deliveries land before counting. A second
	// subscription delivers on the same signal, so a duplicate arrives
	// in this window rather than much later.
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < callbacks; i++ {
		if got := counts[i].Load(); got != 1 {
			t.Errorf("callback %d fired %d times for one SIGWINCH, want 1; installResizeHandler subscribed more than once", i, got)
		}
	}
}

// A detached callback does not fire on a real signal either. The detach
// has to remove the entry the signal goroutine reads, not a copy of it.
func TestSIGWINCHSkipsDetachedCallback(t *testing.T) {
	p := NewProcTerm()

	var kept, dropped atomic.Int32
	keptFired := make(chan struct{}, 4)
	p.OnResize(func() {
		kept.Add(1)
		select {
		case keptFired <- struct{}{}:
		default:
		}
	})
	detach := p.OnResizeDetach(func() { dropped.Add(1) })
	detach()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("send SIGWINCH: %v", err)
	}

	select {
	case <-keptFired:
	case <-time.After(5 * time.Second):
		t.Fatal("the kept callback never fired")
	}
	time.Sleep(200 * time.Millisecond)

	if got := dropped.Load(); got != 0 {
		t.Errorf("detached callback fired %d times on SIGWINCH, want 0", got)
	}
}
