// Package relaunch implements Tier-1 self-restart: replacing the running
// process image with the currently-installed binary via exec(2), no external
// supervisor. It captures the launch state (executable, argv, environment)
// early and, on Trigger, re-execs the same binary with the same arguments —
// so an in-place install (an atomic rename over the same path, which
// `go install` / `just install-dev` already do) is picked up by the next
// restart. On success the OS process is replaced (PID preserved); exec returns
// only on failure.
//
// The capability is opt-in (Enable, wired to --allow-restart) and the
// concrete trigger + pre-exec notification live with the caller (see the web
// mode). This package depends only on the leaf buildinfo package so any mode
// can drive it.
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

	"terva.sh/terva/packages/buildinfo"
)

// TERVA_RELAUNCH_* vars carry the outgoing image's handoff (SetHandoff)
// across the exec boundary — the only channel that survives the image
// replacement. Injected fresh into the exec env on every Trigger, read back
// (and scrubbed) in capture. The FROM key is stamped automatically with the
// outgoing build's display string so the new process can report "restarted
// from vX"; callers add theirs (e.g. the TUI's SESSION) via SetHandoff.
const (
	envHandoffPrefix = "TERVA_RELAUNCH_"
	handoffKeyFrom   = "FROM"
)

var (
	// ErrDisabled is returned when a restart is requested but the capability
	// was never enabled (the operator did not pass --allow-restart).
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
	// handoffIn is what the PREVIOUS image handed this one across the exec
	// (keyed without the env prefix); handoffOut is what this image will hand
	// the NEXT one. Guarded by mu; handoffIn is written only in capture.
	handoffIn  map[string]string
	handoffOut = map[string]string{}
)

func init() { capture() }

func capture() {
	launchExe, captureErr = os.Executable()
	// os.Args / os.Environ are pristine at init and terva never os.Chdir's, so
	// re-execing with them reproduces the original invocation faithfully.
	launchArgs = append([]string(nil), os.Args...)
	launchEnv = append([]string(nil), os.Environ()...)
	// A restarted image finds its predecessor's handoff (version, session, …)
	// in TERVA_RELAUNCH_* vars. Scrub them everywhere after reading: from the
	// process env so tool/extension children don't inherit them, and from the
	// captured exec env so a later restart hands fresh values (re-injected in
	// Trigger), not stale ones.
	handoffIn = map[string]string{}
	for _, kv := range launchEnv {
		if !strings.HasPrefix(kv, envHandoffPrefix) {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimPrefix(kv, envHandoffPrefix), "=")
		if k != "" && v != "" {
			handoffIn[k] = v
		}
		os.Unsetenv(envHandoffPrefix + k)
	}
	launchEnv = dropEnvPrefix(launchEnv, envHandoffPrefix)
}

// dropEnvPrefix returns env without any entry whose key starts with prefix.
func dropEnvPrefix(env []string, prefix string) []string {
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// SetHandoff records a key/value the NEXT image will read back via Handoff
// after a Trigger — the exec env is the only channel that survives the image
// replacement. Keys are short upper-snake tokens ("SESSION"); the "FROM"
// key is reserved (Trigger stamps the outgoing build's version on it).
// Callers may set values at any time, including inside an OnPreExec hook —
// hooks run before the deferred exec injects the env.
func SetHandoff(key, value string) {
	mu.Lock()
	handoffOut[key] = value
	mu.Unlock()
}

// Handoff returns what the previous image handed this one under key, or ""
// when the process was not started by a self-restart (or the prior image set
// nothing for key).
func Handoff(key string) string { return handoffIn[key] }

// PrevVersion returns the build display string of the process image this one
// replaced via Trigger, or "" when the process was not started by a
// self-restart (or the prior build recorded no version).
func PrevVersion() string { return Handoff(handoffKeyFrom) }

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

// Supported reports whether this platform can self-restart at all (exec(2)
// replaces the image on unix; Windows cannot). Callers use it to avoid
// advertising a restart control that could only ever fail — Trigger still
// enforces it via preflight regardless.
func Supported() bool { return supported }

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
	cur := buildinfo.Get().String()
	running := ""
	if cur != "" {
		running = " (running v" + cur + ")"
	}
	fmt.Fprintf(os.Stderr, "relaunch: restart requested (%s); re-exec %s in %s%s\n", reason, launchExe, Delay, running)
	runPreExec(reason)
	afterFunc(Delay, func() {
		// Hand the outgoing image's state to the next one (see
		// envHandoffPrefix); capture() already scrubbed any stale entries,
		// and the pre-exec hooks above have had their chance to SetHandoff.
		mu.Lock()
		if cur != "" {
			handoffOut[handoffKeyFrom] = cur
		}
		launchEnv = dropEnvPrefix(launchEnv, envHandoffPrefix)
		for k, v := range handoffOut {
			if v != "" {
				launchEnv = append(launchEnv, envHandoffPrefix+k+"="+v)
			}
		}
		mu.Unlock()
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
