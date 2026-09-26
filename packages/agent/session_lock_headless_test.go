package agent

import (
	"errors"
	"os"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// TestALockedSessionIsFatalEvenWhenTheRunNamedNoTarget covers the path that
// made this dangerous.
//
// headlessSessionErr warns and returns nil for a run that did not name an
// explicit session, and the caller then proceeds with a nil *Session — a whole
// turn with no persistence at all. That is defensible for "the transcript is
// corrupt". It is indefensible for "another terva is writing this right now",
// which is the one case where carrying on unpersisted is the worst answer
// available. --continue and the resume picker both reach here.
func TestALockedSessionIsFatalEvenWhenTheRunNamedNoTarget(t *testing.T) {
	locked := &testBusyError{}

	// The shape --continue produces: no ResumeID, no Session path.
	if err := headlessSessionErr(build.Args{Continue: true}, locked); err == nil {
		t.Fatal("a locked session was swallowed, so the run would proceed with no persistence")
	}
	if err := headlessSessionErr(build.Args{Continue: true}, locked); !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("the returned error lost its identity: %v", err)
	}
}

func TestAnOrdinarySessionFailureStillWarnsAndContinues(t *testing.T) {
	// The guard above must not turn every open failure fatal; the
	// warn-and-continue behaviour for a corrupt or missing transcript is
	// long-standing and deliberate.
	if err := headlessSessionErr(build.Args{Continue: true}, os.ErrNotExist); err != nil {
		t.Fatalf("an ordinary failure became fatal: %v", err)
	}
}

func TestAnExplicitTargetStaysFatal(t *testing.T) {
	if err := headlessSessionErr(build.Args{ResumeID: "x"}, os.ErrNotExist); err == nil {
		t.Fatal("--resume with an unresolvable id must not land on a fresh transcript")
	}
}

// TestALiveSessionRefusesASecondOpenEndToEnd proves the wiring, not just the
// triage: a real session held by this process refuses a real second open.
func TestALiveSessionRefusesASecondOpenEndToEnd(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := session.NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	second, _, err := session.OpenSession(s.Path)
	if !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("a held session must refuse a second open, got %v", err)
	}
	// A refusal must hand back nothing. A returned handle would be a write
	// handle nobody closes, which is the leak the lock was meant to end.
	if second != nil {
		t.Fatal("a refused open returned a session handle anyway")
	}
}

// testBusyError is a stand-in that unwraps to ErrSessionLocked, so the triage
// is tested without depending on the exact shape sessionlock returns.
type testBusyError struct{}

func (e *testBusyError) Error() string { return "session is open in another terva" }
func (e *testBusyError) Unwrap() error { return session.ErrSessionLocked }
