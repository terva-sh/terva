package workspace

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never observed: %s", what)
}

// Two approvals parked concurrently — a host_tool_call door's racing a model
// call's — resolve independently, each by its own call id. Before the gate
// passed ids down (ConfirmWithCall), both parks borrowed one session-recorded
// curCallID: the second overwrote the first's pending channel, the answer
// reached only the winner, and the loser wedged until turn cancel. Verified
// against the pre-fix code by this test's repro ancestor.
func TestWebConfirmerConcurrentParksDistinct(t *testing.T) {
	s := newTestSession()
	c := &webConfirmer{s: s}

	r1 := make(chan core.ConfirmDecision, 1)
	r2 := make(chan core.ConfirmDecision, 1)
	go func() { r1 <- c.ConfirmWithCall(context.Background(), "bash", "model call", "call-A") }()
	go func() { r2 <- c.ConfirmWithCall(context.Background(), "bash", "host tool call", "hostcall-ext-1") }()
	waitCond(t, "both Confirms parked under their own ids", func() bool {
		return s.permPark.Len() == 2
	})

	s.approve("hostcall-ext-1", core.ConfirmDecision{Allow: false, Reason: "nope"})
	s.approve("call-A", core.ConfirmDecision{Allow: true})

	select {
	case d := <-r1:
		if !d.Allow {
			t.Fatalf("model call got the wrong answer: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("model-call Confirm did not resolve")
	}
	select {
	case d := <-r2:
		if d.Allow || d.Reason != "nope" {
			t.Fatalf("host call got the wrong answer: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host-call Confirm did not resolve")
	}
}

// An id-less fallback Confirm still parks under a unique minted key — two of
// them can never collide either.
func TestWebConfirmerIDLessFallbackMintsUnique(t *testing.T) {
	s := newTestSession()
	c := &webConfirmer{s: s}
	r1 := make(chan core.ConfirmDecision, 1)
	r2 := make(chan core.ConfirmDecision, 1)
	go func() { r1 <- c.Confirm(context.Background(), "bash", "first") }()
	go func() { r2 <- c.Confirm(context.Background(), "bash", "second") }()
	waitCond(t, "both id-less Confirms parked separately", func() bool {
		return s.permPark.Len() == 2
	})
	s.mu.Lock()
	for id := range s.permReq {
		go s.approve(id, core.ConfirmDecision{Allow: true})
	}
	s.mu.Unlock()
	for i, ch := range []chan core.ConfirmDecision{r1, r2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("id-less Confirm %d did not resolve", i+1)
		}
	}
}

// A worker's approval request must carry the worker id as a first-class field
// (PermissionRequest.Agent), not only baked into the callID string convention
// ("worker-<id>-<seq>"): a board correlates the ask to the worker's lane tile
// off the field, never by parsing the callID. Regression guard for R1 of the
// orchestration reconciliation
// (docs/reviews/2026-07-15-eaw-review-from-orchestration-frontend.md).
func TestWorkerConfirmerCarriesAgentID(t *testing.T) {
	s := newTestSession()
	sub := s.hub.add(nil, false)

	c := &workerConfirmer{s: s, ctx: t.Context(), agentID: "wk7"}
	go c.Confirm(context.Background(), "bash", "worker wk7: ls -la")

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventPermissionRequest {
		t.Fatalf("want permission_request, got %q", ev.Type)
	}
	if ev.Permission == nil {
		t.Fatal("permission_request carried no PermissionRequest payload")
	}
	if ev.Permission.Agent != "wk7" {
		t.Fatalf("PermissionRequest.Agent = %q, want the worker id %q", ev.Permission.Agent, "wk7")
	}
	// Unblock the parked Confirm so its goroutine exits cleanly.
	s.approve(ev.Permission.CallID, core.ConfirmDecision{Allow: true})
}
