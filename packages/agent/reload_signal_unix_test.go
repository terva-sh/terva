//go:build unix

package agent

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// writerFunc adapts a function to io.Writer so a test can observe when the
// reload handler actually wrote its log line — a synchronization point that
// beats sleeping and hoping the signal was delivered.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The reason the handler is installed even when restart is off: an unhandled
// SIGHUP terminates the process, so a `systemctl reload` against a daemon
// started without --allow-restart would kill it. With the handler installed and
// restart disabled, delivering SIGHUP to ourselves must NOT kill the test
// binary — it must be swallowed (logged, no re-exec). The injected writer lets
// us wait for the handler to actually run rather than race a timer, and the
// injected trigger proves no restart was attempted.
func TestInstallReloadHandlerSwallowsSIGHUP(t *testing.T) {
	handled := make(chan struct{}, 1)
	w := writerFunc(func(p []byte) (int, error) {
		select {
		case handled <- struct{}{}:
		default:
		}
		return len(p), nil
	})
	triggered := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installReloadHandlerWith(ctx, w,
		func() bool { return false }, // restart disabled -> swallow branch
		func(string) error { triggered <- struct{}{}; return nil })

	// signal.Notify (inside installReloadHandlerWith) has already diverted SIGHUP
	// from its default terminate disposition by the time it returns.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("raise SIGHUP: %v", err)
	}

	select {
	case <-handled: // the handler ran; the process survived a signal that would otherwise kill it
	case <-triggered:
		t.Fatal("a disabled reload must not attempt a restart")
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP was not handled within 2s")
	}
}
