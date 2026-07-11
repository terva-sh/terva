package e2e

import (
	"fmt"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// These tests pin the explicit-compact terminal-event contract: every outcome
// (no-op, success, failure) emits at most one result event and then exactly one
// terminal "done", so a generic RPC loop keyed on "done" — the same signal a
// prompt uses — never waits forever. Before the fix, runCompact returned
// without "done" on every path, and a real failure used a non-canonical
// "message" field.

func okTurnScript() sseScript {
	return sseScript{
		chunks:   []map[string]any{textChunk("ok"), finishChunk("stop", 5, 1)},
		sendDone: true,
	}
}

// promptTurn runs one full prompt turn and drains it to its terminal done.
func promptTurn(t *testing.T, s *rpcSession, id, msg string) {
	t.Helper()
	s.send(map[string]any{"type": "prompt", "id": id, "message": msg})
	ack := s.next()
	if ack["type"] != "response" || ack["command"] != "prompt" || ack["success"] != true {
		t.Fatalf("bad prompt ack: %v", ack)
	}
	s.collectUntil(func(f map[string]any) bool { return f["type"] == "done" })
}

// compactAck sends a compact request and consumes its started-ack.
func compactAck(t *testing.T, s *rpcSession, id string) {
	t.Helper()
	s.send(map[string]any{"type": "compact", "id": id})
	ack := s.next()
	if ack["type"] != "response" || ack["command"] != "compact" || ack["success"] != true {
		t.Fatalf("bad compact ack: %v", ack)
	}
}

// TestRPCCompactNoopEndsWithDone: an explicit compact with nothing to summarize
// must still emit compact_done AND a terminal done (the done was previously
// absent, hanging any loop that waits for it), and leave the server responsive.
func TestRPCCompactNoopEndsWithDone(t *testing.T) {
	// A no-op compact never calls the provider, so no scripts are needed.
	fp := newFakeProvider(t)
	s := startRPC(t, fp.url(), testsupport.TempDir(t))

	compactAck(t, s, "1")
	frames := s.collectUntil(func(f map[string]any) bool { return f["type"] == "done" })

	var sawCompactDone, sawDone bool
	for _, f := range frames {
		switch f["type"] {
		case "compact_done":
			sawCompactDone = true
		case "done":
			sawDone = true
		case "error":
			t.Errorf("unexpected error frame on no-op compact: %v", f)
		}
	}
	if !sawCompactDone {
		t.Errorf("no compact_done before done; frames: %v", frames)
	}
	if !sawDone {
		t.Errorf("no terminal done after no-op compact (the bug this fixes); frames: %v", frames)
	}

	// The server must remain responsive: turnMu released, stream not wedged.
	s.send(map[string]any{"type": "get_state", "id": "2"})
	if st := s.next(); st["type"] != "response" || st["id"] != "2" {
		t.Fatalf("server unresponsive after no-op compact: %v", st)
	}
}

// TestRPCCompactSuccessEndsWithDone: after enough turns to push messages past
// keepTail (4), an explicit compact summarizes and must emit compact_done with
// the model summary, then a terminal done.
func TestRPCCompactSuccessEndsWithDone(t *testing.T) {
	// 4 prompt turns (8 messages) consume 4 scripts; the compact's single
	// summarization request consumes the 5th.
	fp := newFakeProvider(t,
		okTurnScript(), okTurnScript(), okTurnScript(), okTurnScript(),
		sseScript{chunks: []map[string]any{textChunk("SUMMARY"), finishChunk("stop", 9, 1)}, sendDone: true},
	)
	s := startRPC(t, fp.url(), testsupport.TempDir(t))

	for i := 0; i < 4; i++ {
		promptTurn(t, s, fmt.Sprintf("p%d", i), "hi")
	}

	compactAck(t, s, "c")
	frames := s.collectUntil(func(f map[string]any) bool { return f["type"] == "done" })

	var summary string
	var sawDone bool
	for _, f := range frames {
		switch f["type"] {
		case "compact_done":
			summary, _ = f["summary"].(string)
		case "done":
			sawDone = true
		case "error":
			t.Errorf("unexpected error frame on successful compact: %v", f)
		}
	}
	if !sawDone {
		t.Errorf("no terminal done after successful compact; frames: %v", frames)
	}
	if summary == "" {
		t.Errorf("compact_done summary empty; want the model summary. frames: %v", frames)
	}
}

// TestRPCCompactFailureUsesCanonicalErrorThenDone: when the summarization
// request fails, the error must ride the canonical "error" field (not the old
// "message"), followed by exactly one terminal done.
func TestRPCCompactFailureUsesCanonicalErrorThenDone(t *testing.T) {
	// 4 successful turns, then the summarization request gets a hard 400
	// (non-transient, so no retry) — Compact fails.
	fp := newFakeProvider(t,
		okTurnScript(), okTurnScript(), okTurnScript(), okTurnScript(),
		sseScript{httpStatus: 400},
	)
	s := startRPC(t, fp.url(), testsupport.TempDir(t))

	for i := 0; i < 4; i++ {
		promptTurn(t, s, fmt.Sprintf("p%d", i), "hi")
	}

	compactAck(t, s, "c")
	frames := s.collectUntil(func(f map[string]any) bool { return f["type"] == "done" })

	var sawError, sawDone, usedLegacyMessage bool
	var errField any
	for _, f := range frames {
		switch f["type"] {
		case "error":
			sawError = true
			errField = f["error"]
			if _, ok := f["message"]; ok {
				usedLegacyMessage = true
			}
		case "done":
			sawDone = true
		}
	}
	if !sawError {
		t.Errorf("no error frame on failed compact; frames: %v", frames)
	}
	if errField == nil || errField == "" {
		t.Errorf("error frame missing the canonical 'error' field; frames: %v", frames)
	}
	if usedLegacyMessage {
		t.Errorf("error frame used the legacy 'message' field instead of 'error'; frames: %v", frames)
	}
	if !sawDone {
		t.Errorf("no terminal done after failed compact; frames: %v", frames)
	}
}
