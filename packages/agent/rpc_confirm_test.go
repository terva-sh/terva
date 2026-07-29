package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// syncBuf is a mutex-guarded io.Writer so a test can read frames the server
// writes from another goroutine (Confirm blocks on the prompt goroutine while
// the test drives the read loop).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitForAsk polls out for an `ask` frame and returns its id.
func waitForAsk(t *testing.T, out *syncBuf) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var f struct {
				Type, ID, Tool, Preview string
			}
			if json.Unmarshal([]byte(line), &f) == nil && f.Type == "ask" {
				if f.Tool == "" || f.Preview == "" {
					t.Errorf("ask frame missing tool/preview: %s", line)
				}
				return f.ID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no ask frame emitted; got:\n%s", out.String())
	return ""
}

// TestRPCConfirmAsksAndAwaitsApprove is the rpc-native carrier's core: Confirm
// emits an ask frame and blocks until the driver's matching approve arrives,
// then returns exactly that decision. This is what replaces refuse-by-default.
func TestRPCConfirmAsksAndAwaitsApprove(t *testing.T) {
	for _, tc := range []struct {
		name       string
		approve    string
		wantAllow  bool
		wantReason string
	}{
		{"allow", `{"allow":true}`, true, ""},
		{"deny with reason", `{"allow":false,"reason":"rm -rf looks dangerous"}`, false, "rm -rf looks dangerous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &syncBuf{}
			s := &rpcServer{ctx: context.Background(), out: out}

			got := make(chan core.ConfirmDecision, 1)
			go func() { got <- s.Confirm(context.Background(), "bash", "rm -rf /tmp/x") }()

			id := waitForAsk(t, out)
			// The driver answers on the read loop, exactly as run() would. dispatch
			// takes the ask id as its command-id param; the payload carries only the
			// decision fields.
			s.dispatch("approve", id, []byte(tc.approve))

			select {
			case d := <-got:
				if d.Allow != tc.wantAllow {
					t.Errorf("Allow = %v, want %v", d.Allow, tc.wantAllow)
				}
				if d.Reason != tc.wantReason {
					t.Errorf("Reason = %q, want %q", d.Reason, tc.wantReason)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Confirm never returned after approve")
			}
		})
	}
}

// TestRPCConfirmRememberFlagsCross proves a driver can grant a session-scoped
// "always" in the same round trip — the decision carries through to the gate.
func TestRPCConfirmRememberFlagsCross(t *testing.T) {
	out := &syncBuf{}
	s := &rpcServer{ctx: context.Background(), out: out}
	got := make(chan core.ConfirmDecision, 1)
	go func() { got <- s.Confirm(context.Background(), "read", "/etc/hosts") }()
	id := waitForAsk(t, out)
	s.dispatch("approve", id, []byte(`{"type":"approve","id":"`+id+`","allow":true,"remember_tool":true}`))
	d := <-got
	if !d.Allow || !d.RememberTool {
		t.Errorf("decision = %+v, want Allow+RememberTool", d)
	}
}

// TestRPCApproveUnknownIDErrors: a stale or duplicate approve is reported, not
// dropped silently — the driver learns its answer landed nowhere.
func TestRPCApproveUnknownIDErrors(t *testing.T) {
	out := &syncBuf{}
	s := &rpcServer{ctx: context.Background(), out: out}
	s.dispatch("approve", "ask-999", []byte(`{"type":"approve","id":"ask-999","allow":true}`))
	var f struct {
		Type, Command string
		Success       bool
	}
	last := lastFrame(t, out)
	if json.Unmarshal([]byte(last), &f) != nil || f.Type != "response" || f.Command != "approve" || f.Success {
		t.Errorf("want a failed approve response, got: %s", last)
	}
}

// TestRPCConfirmCancelledDenies: when the session context is cancelled while a
// tool waits for approval, Confirm unwinds with a deny rather than hanging the
// shutdown.
func TestRPCConfirmCancelledDenies(t *testing.T) {
	out := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	s := &rpcServer{ctx: ctx, out: out}
	got := make(chan core.ConfirmDecision, 1)
	go func() { got <- s.Confirm(context.Background(), "bash", "sleep 100") }()
	waitForAsk(t, out)
	cancel()
	select {
	case d := <-got:
		if d.Allow {
			t.Error("a cancelled approval must deny, not allow")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not unwind on context cancel")
	}
}

// TestRPCAbortUnparksPendingApproval: `abort` must answer a parked approval,
// not just cancel the turn context — the sharpest of the three turn-cancel gaps
// found in the 2026-07 confirmer survey.
//
// The mechanism changed under this test and the property did not. It used to
// hold because abort swept every parked ask (asks.CancelAll), which was the only
// thing that could reach a Confirm blocking outside the turn's context. Confirm
// now takes the turn's context, so what abort has to do is exactly what it
// already did for the turn itself: cancel it. The test wires the turn cancel the
// way runPrompt does, so it exercises the real path rather than the sweep.
func TestRPCAbortUnparksPendingApproval(t *testing.T) {
	out := &syncBuf{}
	s := &rpcServer{ctx: context.Background(), out: out}
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	s.setCancel(cancelTurn)

	got := make(chan core.ConfirmDecision, 1)
	go func() { got <- s.Confirm(turnCtx, "bash", "sleep 100") }()
	waitForAsk(t, out)

	s.dispatch("abort", "abort-1", nil)

	select {
	case d := <-got:
		if d.Allow {
			t.Error("an aborted approval must deny, not allow")
		}
		if !strings.Contains(d.Reason, "aborted") {
			t.Errorf("Reason = %q, want it to name the abort", d.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm stayed parked across abort — the gap this test pins")
	}
}

// TestRPCGateRoutesThroughAskApprove is the whole point, end to end: a
// core.ConfirmGate backed by the rpc server routes a would-be-refused tool call
// through the wire and honours the driver's verdict. This is the "5th carrier"
// filling the nil-inner hole — Check now asks instead of refusing.
func TestRPCGateRoutesThroughAskApprove(t *testing.T) {
	out := &syncBuf{}
	s := &rpcServer{ctx: context.Background(), out: out}
	gate := core.NewConfirmGate(s) // no policy: every not-yet-allowed call asks the inner Confirmer

	type res struct {
		allow  bool
		reason string
	}
	got := make(chan res, 1)
	go func() {
		allow, reason, _ := gate.Check(context.Background(), "bash", nil, "rm -rf /tmp/x", "")
		got <- res{allow, reason}
	}()

	id := waitForAsk(t, out)
	s.dispatch("approve", id, []byte(`{"allow":false,"reason":"declined"}`))

	select {
	case r := <-got:
		if r.allow {
			t.Error("gate allowed a call the driver denied")
		}
		if r.reason != "declined" {
			t.Errorf("gate reason = %q, want the driver's %q", r.reason, "declined")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate.Check never returned")
	}
}

func lastFrame(t *testing.T, out *syncBuf) string {
	t.Helper()
	var last string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			last = strings.TrimSpace(line)
		}
	}
	if last == "" {
		t.Fatal("no frame written")
	}
	return last
}
