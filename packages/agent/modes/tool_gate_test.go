package modes

import "testing"

// While paced text is still draining, a tool that arrives mid-stream
// must stay gated until the painted text reaches the position
// captured when the tool started.
func TestToolGateHoldsToolUntilTextDrains(t *testing.T) {
	s := newStreamState()

	// Simulate a turn that streamed 10 runes already with 20 more
	// queued in the pacer when the tool call arrives.
	s.beginTurn()
	s.painted.WriteString("0123456789") // 10 painted
	s.pending = []rune("01234567890123456789")

	s.gateTool("t1")
	if got := s.gates["t1"]; got != 30 {
		t.Fatalf("gate = %d, want 30 (10 painted + 20 pending)", got)
	}

	if s.gateOpen("t1") {
		t.Fatal("tool should be gated while only 10/30 runes painted")
	}

	// Drain halfway: still gated.
	s.painted.WriteString("0123456789") // 20 painted
	if s.gateOpen("t1") {
		t.Fatal("tool should still be gated at 20/30")
	}

	// Reach the gate: now visible.
	s.painted.WriteString("0123456789") // 30 painted
	if !s.gateOpen("t1") {
		t.Fatal("tool should be visible once painted text reaches the gate")
	}
}

// A tool call on a turn with no active text stream shows immediately.
func TestToolGateOpenWhenNoStream(t *testing.T) {
	s := newStreamState()

	s.gateTool("t1")
	if got := s.gates["t1"]; got != 0 {
		t.Fatalf("gate = %d, want 0 for non-streaming turn", got)
	}
	if !s.gateOpen("t1") {
		t.Fatal("tool should be visible immediately when no text is streaming")
	}
}

// First registration wins: a later EvToolCall must not move an
// existing gate (e.g. push it forward as more text arrives).
func TestToolGateFirstRegistrationWins(t *testing.T) {
	s := newStreamState()
	s.beginTurn()
	s.painted.WriteString("01234") // 5
	s.pending = []rune("01234")

	s.gateTool("t1") // gate 10
	first := s.gates["t1"]

	// More text queues up, then the same tool is re-registered.
	s.pending = append(s.pending, []rune("567890")...)
	s.gateTool("t1")

	if s.gates["t1"] != first {
		t.Fatalf("gate moved from %d to %d; first registration must win", first, s.gates["t1"])
	}
}

// Once streaming finalizes, the painted buffer resets to length 0;
// gates that were already satisfied must not re-hide their tools.
func TestOpenAllToolGatesSurvivesStreamReset(t *testing.T) {
	s := newStreamState()
	s.beginTurn()
	s.painted.WriteString("0123456789")
	s.gateTool("t1") // gate 10, satisfied

	if !s.gateOpen("t1") {
		t.Fatal("precondition: tool should be open before reset")
	}

	// Finalize the stream: reset() opens gates before clearing the
	// painted buffer.
	s.reset()

	if !s.gateOpen("t1") {
		t.Fatal("tool re-hidden after stream reset; gate should have been opened")
	}
}

// The full happy-path lifecycle: deltas arrive, the message
// finalizes mid-drain (flushing latch), and the pacer finishes the
// reveal.
func TestStreamStateFlushLifecycle(t *testing.T) {
	s := newStreamState()
	s.beginAssistant()
	s.appendDelta("hello world")

	if !s.active() || s.flushing() {
		t.Fatalf("after deltas: active=%v flushing=%v, want live", s.active(), s.flushing())
	}

	// Final message lands while 11 runes are still queued: deferred.
	if !s.finishMessage() {
		t.Fatal("finishMessage should defer while runes are pending")
	}
	if !s.flushing() || !s.active() {
		t.Fatal("state should be flushing (and still active) after deferred finish")
	}

	// Pacer drains in batches of 6: 6, then 5.
	if painted, finished := s.paceTick(6); !painted || finished {
		t.Fatalf("tick1: painted=%v finished=%v", painted, finished)
	}
	if got := s.visible(); got != "hello " {
		t.Fatalf("visible = %q after first tick", got)
	}
	if painted, finished := s.paceTick(6); !painted || finished {
		t.Fatalf("tick2: painted=%v finished=%v", painted, finished)
	}
	// Buffer empty + flushing: the next tick completes the reveal.
	if painted, finished := s.paceTick(6); painted || !finished {
		t.Fatalf("tick3: painted=%v finished=%v, want finish", painted, finished)
	}
	if s.active() || s.visible() != "" {
		t.Fatal("stream should be idle and empty after the flush completes")
	}
	// Idle ticks are no-ops.
	if painted, finished := s.paceTick(6); painted || finished {
		t.Fatal("idle tick should report nothing")
	}
}

// finishMessage with nothing queued (full-replay sessions, abort
// paths) resets synchronously instead of deferring.
func TestStreamStateFinishWithoutBacklog(t *testing.T) {
	s := newStreamState()
	s.beginAssistant()
	s.appendDelta("hi")
	s.paceTick(10) // drain fully

	if s.finishMessage() {
		t.Fatal("finishMessage should not defer with an empty backlog")
	}
	if s.active() {
		t.Fatal("stream should be idle after synchronous finish")
	}
}

// promptReturned must leave a draining stream alone (the pacer owns
// the shutdown) but retire an already-drained one.
func TestStreamStatePromptReturned(t *testing.T) {
	s := newStreamState()
	s.beginAssistant()
	s.appendDelta("queued")
	s.promptReturned()
	if !s.active() {
		t.Fatal("promptReturned must not kill a stream with queued runes")
	}

	s.paceTick(100)
	s.promptReturned()
	if s.active() {
		t.Fatal("promptReturned should retire a fully-drained stream")
	}
}
