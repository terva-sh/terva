//go:build unix

// These tests exercise the unix syscall.Exec relaunch path (execImage/afterFunc
// seams, go-run path detection). On non-unix (the Windows CI runner) relaunch is
// unsupported (relaunch_other.go returns ErrUnsupported and go-run paths differ),
// so the tests don't apply there — gate them to unix like relaunch_unix.go.
package relaunch

import (
	"errors"
	"testing"
	"time"
)

// withStubbedExec swaps the exec/timer indirections for synchronous test doubles
// and restores them (and all mutable state) on cleanup, so tests never replace
// the test process and don't leak state into each other. afterFunc runs its
// callback inline; a stubbed execImage that returns nil models a successful
// exec (which, in production, never returns).
func withStubbedExec(t *testing.T, exec func() error) *int {
	t.Helper()
	calls := 0
	origExec, origAfter, origEnabled := execImage, afterFunc, enabled
	origPre, origFail, origDelay := preExec, onFailure, Delay
	origExe, origErr := launchExe, captureErr
	execImage = func() error { calls++; return exec() }
	afterFunc = func(_ time.Duration, fn func()) *time.Timer { fn(); return nil }
	Delay = 0
	// The test process is itself a `go test` temp binary, which preflight would
	// (correctly) reject — pretend we're an installed binary.
	launchExe, captureErr = "/usr/local/bin/terva", nil
	t.Cleanup(func() {
		execImage, afterFunc, enabled = origExec, origAfter, origEnabled
		preExec, onFailure, Delay = origPre, origFail, origDelay
		launchExe, captureErr = origExe, origErr
		restarting.Store(false)
	})
	return &calls
}

func TestTriggerDisabled(t *testing.T) {
	withStubbedExec(t, func() error { return nil })
	mu.Lock()
	enabled = false
	mu.Unlock()
	if err := Trigger("x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

func TestTriggerRunsHooksThenExec(t *testing.T) {
	calls := withStubbedExec(t, func() error { return nil }) // nil = successful exec
	Enable()
	var gotReason string
	OnPreExec(func(r string) { gotReason = r })

	if err := Trigger("code change"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if gotReason != "code change" {
		t.Errorf("pre-exec hook reason = %q, want %q", gotReason, "code change")
	}
	if *calls != 1 {
		t.Errorf("execImage called %d times, want 1", *calls)
	}
	if !Restarting() {
		t.Error("Restarting() should stay true after a successful exec")
	}
}

func TestTriggerInProgress(t *testing.T) {
	withStubbedExec(t, func() error { return nil })
	Enable()
	if err := Trigger("first"); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	if err := Trigger("second"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("want ErrInProgress on second trigger, got %v", err)
	}
}

func TestTriggerExecFailureRunsOnFailureAndResets(t *testing.T) {
	boom := errors.New("boom")
	withStubbedExec(t, func() error { return boom })
	Enable()
	var gotErr error
	OnFailure(func(err error) { gotErr = err })

	if err := Trigger("x"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !errors.Is(gotErr, boom) {
		t.Errorf("OnFailure err = %v, want boom", gotErr)
	}
	if Restarting() {
		t.Error("Restarting() should reset to false after exec failure")
	}
}

func TestIsGoRun(t *testing.T) {
	cases := map[string]bool{
		"/usr/local/bin/terva":                 false,
		"/Users/x/go/bin/terva":                false,
		"/tmp/go-build123/b001/exe/terva":      true,
		"/var/folders/xx/go-build99/exe/terva": true,
		"/home/x/.cache/go-tmp/exe/terva":      true,
		"/home/x/project/__debug_bin1234567":   true,
	}
	for path, want := range cases {
		if got := isGoRun(path); got != want {
			t.Errorf("isGoRun(%q) = %v, want %v", path, got, want)
		}
	}
}
