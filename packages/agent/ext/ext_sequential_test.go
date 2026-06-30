package ext

import (
	"encoding/json"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

func toolCall(id, name string) extproto.ToolCallFromHost {
	return extproto.ToolCallFromHost{Type: "tool_call", ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

// TestSequentialPreservesCrossToolOrder is the headline guarantee: two tools
// marked Sequential() share one FIFO lane, so a later call does not start until
// the earlier one finishes — even across different tools (raise-shields then
// fire). The host dispatches both at once; the SDK must not race them.
func TestSequentialPreservesCrossToolOrder(t *testing.T) {
	h := newHarness("seq-ext")
	started := make(chan string, 8)
	gate := make(chan struct{})

	schema := json.RawMessage(`{"type":"object"}`)
	h.ext.Tool("raise", "raise shields", schema, func(json.RawMessage) ToolResult {
		started <- "raise"
		<-gate // hold the lane until the test releases it
		return TextResult("shields up")
	}, Sequential())
	h.ext.Tool("fire", "fire", schema, func(json.RawMessage) ToolResult {
		started <- "fire"
		return TextResult("fired")
	}, Sequential())

	go h.ext.Run()
	h.handshake(t)

	// Issue both calls back-to-back, in order.
	h.sendToExt(t, toolCall("r1", "raise"))
	h.sendToExt(t, toolCall("f1", "fire"))

	// raise starts first.
	if got := <-started; got != "raise" {
		t.Fatalf("first to start = %q, want raise", got)
	}
	// fire must NOT start while raise holds the lane.
	select {
	case got := <-started:
		t.Fatalf("fire started before raise finished (got %q) — calls were not serialized", got)
	case <-time.After(150 * time.Millisecond):
	}
	// Release raise; only now may fire start.
	close(gate)
	if got := <-started; got != "fire" {
		t.Fatalf("second to start = %q, want fire", got)
	}

	// Results come back in arrival order: raise (r1) before fire (f1).
	var ids []string
	for len(ids) < 2 {
		f := h.drainUntil(t, "tool_result")
		var tr extproto.ToolResultFromExt
		if err := json.Unmarshal(f.raw, &tr); err != nil {
			t.Fatalf("unmarshal tool_result: %v", err)
		}
		ids = append(ids, tr.ID)
	}
	if ids[0] != "r1" || ids[1] != "f1" {
		t.Fatalf("result order = %v, want [r1 f1]", ids)
	}
	h.hostW.Close()
}

// TestNonSequentialStaysConcurrent confirms the lane only governs Sequential
// tools: plain tools still run concurrently, so a blocked call doesn't hold up
// the next one.
func TestNonSequentialStaysConcurrent(t *testing.T) {
	h := newHarness("conc-ext")
	started := make(chan string, 8)
	gate := make(chan struct{})

	schema := json.RawMessage(`{"type":"object"}`)
	h.ext.Tool("slow", "blocks", schema, func(json.RawMessage) ToolResult {
		started <- "slow"
		<-gate
		return TextResult("done")
	})
	h.ext.Tool("quick", "fast", schema, func(json.RawMessage) ToolResult {
		started <- "quick"
		return TextResult("done")
	})

	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, toolCall("s1", "slow"))
	h.sendToExt(t, toolCall("q1", "quick"))

	// Both start even though slow is still blocked — they are not serialized.
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case s := <-started:
			got[s] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %v started; a plain tool was blocked behind another", got)
		}
	}
	close(gate)
	h.hostW.Close()
}
