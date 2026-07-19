package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
)

type confirmFunc func(tool, preview string) core.ConfirmDecision

func (f confirmFunc) Confirm(tool, preview string) core.ConfirmDecision { return f(tool, preview) }

// TestRecognizeAndEncodeTervaApproval pins the two rpc ask/approve halves: an
// `ask` event is recognised into an Ask, and a decision round-trips into the
// exact `approve` command shape the server's dispatch reads.
func TestRecognizeAndEncodeTervaApproval(t *testing.T) {
	ev := Event{Type: "ask", Data: map[string]any{"id": "ask-3", "tool": "bash", "preview": "rm -rf /tmp/x"}}
	ask, ok := recognizeTervaAsk(ev)
	if !ok || ask.ID != "ask-3" || ask.Tool != "bash" || ask.Preview != "rm -rf /tmp/x" {
		t.Fatalf("recognize = %+v, ok=%v", ask, ok)
	}
	// A non-ask event, and an ask with no id, are both not approval requests.
	if _, ok := recognizeTervaAsk(Event{Type: "assistant_message"}); ok {
		t.Error("a non-ask event must not be recognised as an approval")
	}
	if _, ok := recognizeTervaAsk(Event{Type: "ask", Data: map[string]any{"tool": "bash"}}); ok {
		t.Error("an ask with no id cannot be answered and must not be recognised")
	}

	frame, err := encodeTervaApprove("ask-3", core.ConfirmDecision{Allow: true, RememberTool: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(frame), "\n") {
		t.Error("the approve command must be a newline-terminated frame")
	}
	var m map[string]any
	if err := json.Unmarshal(frame, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "approve" || m["id"] != "ask-3" || m["allow"] != true || m["remember_tool"] != true {
		t.Errorf("approve frame = %v", m)
	}
}

// TestHandleAskRoutesToConfirmerAndReplies is the caller half end to end: a
// worker's Ask reaches the orchestrator's Confirmer (labelled with the worker
// id so the human knows whose action it is), and the verdict is written back to
// the worker as an approve frame.
func TestHandleAskRoutesToConfirmerAndReplies(t *testing.T) {
	pr, pw := io.Pipe()
	var sawPreview string
	r := &Runner{
		agent:   &swarm.Agent{ID: "flaky-42"},
		backend: tervaBackend(),
		stdin:   pw,
		confirmer: confirmFunc(func(tool, preview string) core.ConfirmDecision {
			sawPreview = preview
			return core.ConfirmDecision{Allow: true}
		}),
	}

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		got <- line
	}()

	r.handleAsk(context.Background(), Ask{ID: "ask-1", Tool: "bash", Preview: "make test"}, nopSink{})

	if !strings.Contains(sawPreview, "worker flaky-42:") {
		t.Errorf("the card must be labelled with the worker id, got preview %q", sawPreview)
	}
	select {
	case frame := <-got:
		var m map[string]any
		if err := json.Unmarshal([]byte(frame), &m); err != nil {
			t.Fatalf("approve frame not json: %q", frame)
		}
		if m["type"] != "approve" || m["id"] != "ask-1" || m["allow"] != true {
			t.Errorf("approve frame = %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no approve frame written to the worker")
	}
}

// TestHandleAskDeniesWhenNoApprover: a worker whose orchestrator is gone (nil
// confirmer) gets a clean deny, not a hang — the safe fallback that keeps a
// gated worker from freezing when no human is watching.
func TestHandleAskDeniesWhenNoApprover(t *testing.T) {
	pr, pw := io.Pipe()
	r := &Runner{agent: &swarm.Agent{ID: "orphan-1"}, backend: tervaBackend(), stdin: pw, confirmer: nil}

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		got <- line
	}()

	r.handleAsk(context.Background(), Ask{ID: "ask-9", Tool: "bash", Preview: "x"}, nopSink{})

	frame := <-got
	var m map[string]any
	_ = json.Unmarshal([]byte(frame), &m)
	if m["allow"] != false {
		t.Errorf("a worker with no approver must be denied, got %v", m)
	}
}

// TestHandleAskCancelDeniesRatherThanHangs: if the worker is stopped while its
// approval is parked on a human, handleAsk unwinds (denying) instead of
// deadlocking the stdout goroutine and, with it, the whole run's teardown.
func TestHandleAskCancelDeniesRatherThanHangs(t *testing.T) {
	pr, pw := io.Pipe()
	blocked := make(chan struct{})
	r := &Runner{
		agent:   &swarm.Agent{ID: "slow-1"},
		backend: tervaBackend(),
		stdin:   pw,
		confirmer: confirmFunc(func(tool, preview string) core.ConfirmDecision {
			close(blocked)
			select {} // never answers — the human walked away
		}),
	}

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		got <- line
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.handleAsk(ctx, Ask{ID: "ask-2", Tool: "bash", Preview: "y"}, nopSink{})
		close(done)
	}()

	<-blocked
	cancel() // the worker is stopped
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleAsk did not unwind when the worker was stopped — teardown would deadlock")
	}
	frame := <-got
	var m map[string]any
	_ = json.Unmarshal([]byte(frame), &m)
	if m["allow"] != false {
		t.Errorf("a stopped worker's ask must deny, got %v", m)
	}
}
