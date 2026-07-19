package worker

import "testing"

// TestCarryTurnErrorFoldsRpcSequence is the regression for the release-review
// finding: terva's rpc reports a failed turn as a standalone `error` event then a
// bare `done`→task_end, so the empty task_end made a failed worker turn read as
// success. carryTurnError must thread the message onto that task_end.
func TestCarryTurnErrorFoldsRpcSequence(t *testing.T) {
	// The exact two-line rpc sequence, already translated: an error, then task_end.
	errEv := Event{Type: "error", Data: map[string]any{"error": "boom: model refused"}}
	endEv := Event{Type: "task_end", Data: map[string]any{}}

	var pending string
	_, pending = carryTurnError(errEv, pending)
	if pending != "boom: model refused" {
		t.Fatalf("error event should be remembered; pending = %q", pending)
	}
	got, pending := carryTurnError(endEv, pending)
	if msg, _ := got.Data["error"].(string); msg != "boom: model refused" {
		t.Fatalf("task_end error = %q; want the carried message", msg)
	}
	if pending != "" {
		t.Fatalf("pending should be cleared after folding into task_end; got %q", pending)
	}
}

// A clean turn (no error event) leaves task_end untouched — no phantom error.
func TestCarryTurnErrorLeavesCleanTaskEnd(t *testing.T) {
	got, pending := carryTurnError(Event{Type: "task_end", Data: map[string]any{}}, "")
	if _, has := got.Data["error"]; has {
		t.Fatalf("a clean task_end must carry no error; got %v", got.Data["error"])
	}
	if pending != "" {
		t.Fatalf("pending should stay empty; got %q", pending)
	}
}

// A backend whose task_end already carries its own error (claude) is not clobbered.
func TestCarryTurnErrorDoesNotClobberExisting(t *testing.T) {
	var pending string
	_, pending = carryTurnError(Event{Type: "error", Data: map[string]any{"error": "standalone"}}, pending)
	got, pending := carryTurnError(Event{Type: "task_end", Data: map[string]any{"error": "native"}}, pending)
	if msg, _ := got.Data["error"].(string); msg != "native" {
		t.Fatalf("task_end error = %q; want its own 'native' (not clobbered)", msg)
	}
	if pending != "" {
		t.Fatalf("pending should be cleared; got %q", pending)
	}
}

// The pending error does not leak past its own task_end into a later clean turn —
// terva's rpc is a long-lived, multi-prompt session.
func TestCarryTurnErrorDoesNotLeakAcrossTurns(t *testing.T) {
	var pending string
	_, pending = carryTurnError(Event{Type: "error", Data: map[string]any{"error": "turn-1 failed"}}, pending)
	_, pending = carryTurnError(Event{Type: "task_end", Data: map[string]any{}}, pending) // consumes it
	// A second turn ends cleanly; it must NOT inherit turn 1's error.
	got, pending := carryTurnError(Event{Type: "task_end", Data: map[string]any{}}, pending)
	if _, has := got.Data["error"]; has {
		t.Fatalf("turn 2's task_end inherited a stale error: %v", got.Data["error"])
	}
	if pending != "" {
		t.Fatalf("pending should be empty; got %q", pending)
	}
}

// End to end through the real translator: the raw rpc lines translate then fold to
// a task_end that carries the error, proving the drain-loop wiring is coherent.
func TestCarryTurnErrorThroughTranslateTerva(t *testing.T) {
	var pending string
	var lastTaskEnd *Event
	for _, line := range []string{`{"type":"error","error":"rpc said no"}`, `{"type":"done"}`} {
		for _, ev := range translateTerva([]byte(line)) {
			ev, pending = carryTurnError(ev, pending)
			if ev.Type == "task_end" {
				e := ev
				lastTaskEnd = &e
			}
		}
	}
	if lastTaskEnd == nil {
		t.Fatal("no task_end synthesized from the done frame")
	}
	if msg, _ := lastTaskEnd.Data["error"].(string); msg != "rpc said no" {
		t.Fatalf("folded task_end error = %q; want 'rpc said no'", msg)
	}
}
