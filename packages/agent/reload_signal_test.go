package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// A reload against a daemon started without --allow-restart must NOT restart,
// and must say why — the whole reason the handler exists is so the signal is
// swallowed rather than terminating the process.
func TestReloadPolicySwallowsWhenRestartDisabled(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	reloadPolicy(&buf, func() bool { return false }, func(string) error { calls++; return nil })

	if calls != 0 {
		t.Fatalf("trigger called %d times while restart is disabled; a reload must never re-exec without --allow-restart", calls)
	}
	if !strings.Contains(buf.String(), "self-restart is off") {
		t.Fatalf("a swallowed reload must explain why nothing happened; got %q", buf.String())
	}
}

// With --allow-restart on, a reload fires exactly one self-restart, and the
// reason names the signal so the operator-facing logs are attributable.
func TestReloadPolicyTriggersWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	var reason string
	calls := 0
	reloadPolicy(&buf, func() bool { return true }, func(r string) error { calls++; reason = r; return nil })

	if calls != 1 {
		t.Fatalf("trigger called %d times; an enabled reload must restart exactly once", calls)
	}
	if !strings.Contains(reason, "SIGHUP") {
		t.Fatalf("trigger reason %q should name the signal", reason)
	}
}

// A restart that cannot proceed (already restarting, a `go run` build, …) must
// be reported, not silently dropped — the operator asked for a reload and needs
// to know it did nothing.
func TestReloadPolicyReportsATriggerFailure(t *testing.T) {
	var buf bytes.Buffer
	reloadPolicy(&buf, func() bool { return true }, func(string) error { return errors.New("already in progress") })

	if !strings.Contains(buf.String(), "already in progress") {
		t.Fatalf("a failed trigger must surface the error; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "continuing to serve") {
		t.Fatalf("a failed reload must reassure that the daemon keeps serving; got %q", buf.String())
	}
}
