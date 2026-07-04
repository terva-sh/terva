// Package relaunch implements Tier-1 self-restart: replacing the running
// process image with the currently-installed binary via exec(2), no external
// supervisor. It captures the launch state (executable, argv, environment)
// early and, on Trigger, re-execs the same binary with the same arguments —
// so an in-place install (an atomic rename over the same path, which
// `go install` / `just install-dev` already do) is picked up by the next
// restart. On success the OS process is replaced (PID preserved); exec returns
// only on failure.
//
// The capability is opt-in (Enable, wired to --web-allow-restart) and the
// concrete trigger + pre-exec notification live with the caller (see the web
// mode). This package stays dependency-free so any mode can drive it.
package relaunch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrDisabled is returned when a restart is requested but the capability
	// was never enabled (the operator did not pass --web-allow-restart).
	ErrDisabled = errors.New("self-restart is disabled")
	// ErrInProgress is returned when a restart is already scheduled.
	ErrInProgress = errors.New("a restart is already in progress")
	// ErrUnsupported is returned on platforms without exec(2) (e.g. Windows).
	ErrUnsupported = errors.New("self-restart is not supported on this platform")
	// ErrGoRun is returned when running from a `go run` temp binary, which is
	// deleted when go run exits and so cannot be re-executed.
	ErrGoRun = errors.New("cannot self-restart a `go run` build; install the binary first")
)

// launch state, captured once before anything can mutate it.
var (
	launchExe  string
	launchArgs []string
	launchEnv  []string
	captureErr error
)

func init() { capture() }

func capture() {
	launchExe, captureErr = os.Executable()
	// os.Args / os.Environ are pristine at init and terva never os.Chdir's, so
	// re-execing with them reproduces the original invocation faithfully.
	launchArgs = append([]string(nil), os.Args...)
	launchEnv = append([]string(nil), os.Environ()...)
}

var (
	mu         sync.Mutex
	enabled    bool
	preExec    []func(reason string)
	onFailure  []func(err error)
	restarting atomic.Bool

	// Delay is how long Trigger waits before replacing the image, giving the
	// caller's ack and the "restarting" notice time to flush to clients.
	Delay = 300 * time.Millisecond

	// execImage replaces the current process image; afterFunc schedules the
	// deferred exec. Both are indirected so tests can drive Trigger without
	// actually replacing the test process.
	execImage = realExec
	afterFunc = time.AfterFunc
)

// Enable turns on the restart capability. Idempotent.
func Enable() {
	mu.Lock()
	enabled = true
	mu.Unlock()
}

// Enabled reports whether the capability is on.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// OnPreExec registers a hook run synchronously (in registration order) the
// moment a restart is triggered, before the deferred exec — use it to notify
// clients and flush logs. Safe to call at setup time.
func OnPreExec(fn func(reason string)) {
	mu.Lock()
	preExec = append(preExec, fn)
	mu.Unlock()
}

// OnFailure registers a hook run if the deferred exec fails (the process keeps
// serving). Use it to tell clients the restart did not happen.
func OnFailure(fn func(err error)) {
	mu.Lock()
	onFailure = append(onFailure, fn)
	mu.Unlock()
}

// Restarting reports whether a restart has been triggered and not since failed.
func Restarting() bool { return restarting.Load() }

// Trigger begins a Tier-1 self-restart: it runs the pre-exec hooks now, then
// after Delay replaces the process image with the currently-installed binary
// (same argv/env). It returns immediately so the caller can ack; the exec
// happens on a timer. It errors — changing nothing — if the capability is
// disabled, a restart is already pending, the platform lacks exec(2), or the
// binary is a `go run` temp build.
func Trigger(reason string) error {
	if !Enabled() {
		return ErrDisabled
	}
	if err := preflight(); err != nil {
		return err
	}
	if !restarting.CompareAndSwap(false, true) {
		return ErrInProgress
	}
	fmt.Fprintf(os.Stderr, "relaunch: restart requested (%s); re-exec %s in %s\n", reason, launchExe, Delay)
	runPreExec(reason)
	afterFunc(Delay, func() {
		err := execImage()
		if err == nil {
			// exec succeeded: the image is replaced and this line is unreachable
			// in production (real syscall.Exec returns only on failure).
			return
		}
		// Exec failed — undo the "restarting" latch and tell the caller's hooks
		// so it can keep serving on the old image.
		restarting.Store(false)
		fmt.Fprintf(os.Stderr, "relaunch: exec failed, continuing to serve: %v\n", err)
		mu.Lock()
		fns := append([]func(error){}, onFailure...)
		mu.Unlock()
		for _, fn := range fns {
			fn(err)
		}
	})
	return nil
}

// preflight validates that a restart can actually succeed, so Trigger can fail
// loudly instead of scheduling an exec that will die.
func preflight() error {
	if captureErr != nil {
		return fmt.Errorf("locate executable: %w", captureErr)
	}
	if !supported {
		return ErrUnsupported
	}
	if isGoRun(launchExe) {
		return ErrGoRun
	}
	return nil
}

// isGoRun detects a `go run` temp binary (mirrors the guard in botcmd.go).
func isGoRun(exe string) bool {
	sep := string(os.PathSeparator)
	return strings.Contains(exe, sep+"go-build") || strings.Contains(exe, sep+"go-tmp") ||
		strings.HasPrefix(filepath.Base(exe), "__debug_bin")
}

func runPreExec(reason string) {
	mu.Lock()
	cp := append([]func(string){}, preExec...)
	mu.Unlock()
	for _, fn := range cp {
		fn(reason)
	}
}
