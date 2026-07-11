package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// These tests pin the INVERSE of TestJSONMode_WriteToolRoundTrip: a mutating
// tool requested in a headless run under --no-yolo (no interactive confirmer)
// must be REFUSED by the shared headless gate — no file written, a
// model-readable is_error tool_result, no wait for a confirmer that will never
// arrive (the 60s runTerva timeout catches a hang), and a clean exit. The gate
// is unit-tested, but only a compiled-binary test proves the wiring per run
// mode; a future mode-specific regression that skipped the gate would leave the
// package tests green and only fail here.

func writeToolScripts() []sseScript {
	args, _ := json.Marshal(map[string]string{"path": "should-not-exist.txt", "content": "nope\n"})
	return []sseScript{
		// Turn 1: the model asks to write a file.
		{
			chunks: []map[string]any{
				toolCallChunk("call_1", "write", string(args)),
				finishChunk("tool_calls", 20, 10),
			},
			sendDone: true,
		},
		// Turn 2: after seeing the refusal, the model wraps up cleanly.
		{
			chunks:   []map[string]any{textChunk("understood"), finishChunk("stop", 30, 2)},
			sendDone: true,
		},
	}
}

func TestJSONMode_RefusesWriteToolUnderNoYolo(t *testing.T) {
	fp := newFakeProvider(t, writeToolScripts()...)
	ws := testsupport.TempDir(t)

	res := runTerva(t, fp.url(), ws, "--json", "--no-yolo", "write a file")

	// (d) clean turn completion — a refusal is not a crash.
	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	// (a) no mutation: the write never happened.
	if _, err := os.Stat(filepath.Join(ws, "should-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("write tool executed despite --no-yolo (file exists): %v", err)
	}

	events := parseEvents(t, res.stdout)
	// (b) model-readable refusal: an is_error tool_result with content.
	result := firstEvent(events, "tool_result")
	if result == nil {
		t.Fatalf("no tool_result event; types: %v", eventTypes(events))
	}
	if isErr, _ := result["is_error"].(bool); !isErr {
		t.Errorf("tool_result is_error = false; a refused tool must be an error result: %v", result)
	}
	if c := result["content"]; c == nil || c == "" {
		t.Errorf("refusal tool_result has empty content; the model can't read why: %v", result)
	}
	// The turn ended cleanly (a terminal turn_end exists, not a wedge).
	if end := lastEvent(events, "turn_end"); end == nil {
		t.Errorf("no turn_end event; types: %v", eventTypes(events))
	}

	// If the agent fed the refusal back to the model (a second turn), that
	// request must carry the role:tool result — confirming it is genuinely
	// model-readable, not just client-visible.
	if reqs := fp.requests(); len(reqs) >= 2 {
		var sawToolResult bool
		for _, m := range reqs[1].Messages {
			if m["role"] == "tool" && m["tool_call_id"] == "call_1" {
				sawToolResult = true
			}
		}
		if !sawToolResult {
			t.Errorf("second request lacks the role:tool refusal for call_1: %v", reqs[1].Messages)
		}
	}
}

func TestPrintMode_RefusesWriteToolUnderNoYolo(t *testing.T) {
	fp := newFakeProvider(t, writeToolScripts()...)
	ws := testsupport.TempDir(t)

	res := runTerva(t, fp.url(), ws, "--print", "--no-yolo", "write a file")

	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(ws, "should-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("write tool executed despite --no-yolo in print mode (file exists): %v", err)
	}
}
