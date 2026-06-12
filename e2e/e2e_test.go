package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parseEvents decodes --json mode's NDJSON stdout. Every line must be
// a valid JSON object — a torn or non-JSON line is itself a failure,
// since the whole point of the mode is machine-readable output.
func parseEvents(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for i, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("json mode line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
		events = append(events, ev)
	}
	return events
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		s, _ := ev["type"].(string)
		out = append(out, s)
	}
	return out
}

func firstEvent(events []map[string]any, typ string) map[string]any {
	for _, ev := range events {
		if ev["type"] == typ {
			return ev
		}
	}
	return nil
}

// lastEvent matters for turn_end: the agent emits one per step, so a
// tool-call prompt yields stop:"tool_use" first and the real outcome
// last.
func lastEvent(events []map[string]any, typ string) map[string]any {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i]["type"] == typ {
			return events[i]
		}
	}
	return nil
}

func hasType(events []map[string]any, typ string) bool {
	return firstEvent(events, typ) != nil
}

func TestPrintMode_FinalText(t *testing.T) {
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("4"), finishChunk("stop", 12, 1)},
		sendDone: true,
	})
	ws := t.TempDir()

	res := runTerva(t, fp.url(), ws, "-p", "what is 2+2?")

	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "4" {
		t.Errorf("print mode stdout = %q, want %q", got, "4")
	}

	reqs := fp.requests()
	if len(reqs) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(reqs))
	}
	if reqs[0].Model != "test-model" {
		t.Errorf("request model = %q, want test-model", reqs[0].Model)
	}
	if !reqs[0].Stream {
		t.Errorf("request did not set stream:true")
	}
	if auths := fp.authHeaders(); auths[0] != "Bearer e2e-test-key" {
		t.Errorf("auth header = %q, want Bearer e2e-test-key", auths[0])
	}
	// The user prompt must actually reach the wire.
	found := false
	for _, m := range reqs[0].Messages {
		if c, _ := m["content"].(string); strings.Contains(c, "what is 2+2?") {
			found = true
		}
	}
	if !found {
		t.Errorf("user prompt not found in request messages: %v", reqs[0].Messages)
	}
}

func TestJSONMode_EventStream(t *testing.T) {
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("hello "), textChunk("world"), finishChunk("stop", 8, 2)},
		sendDone: true,
	})
	ws := t.TempDir()

	res := runTerva(t, fp.url(), ws, "--json", "say hi")

	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	events := parseEvents(t, res.stdout)

	for _, want := range []string{"turn_start", "text_delta", "assistant_message", "turn_end"} {
		if !hasType(events, want) {
			t.Errorf("missing %q event; got types: %v", want, eventTypes(events))
		}
	}
	if hasType(events, "error") {
		t.Errorf("unexpected error event: %v", firstEvent(events, "error"))
	}

	// Deltas must reassemble into the streamed text.
	var text strings.Builder
	for _, ev := range events {
		if ev["type"] == "text_delta" {
			d, _ := ev["delta"].(string)
			text.WriteString(d)
		}
	}
	if text.String() != "hello world" {
		t.Errorf("reassembled deltas = %q, want %q", text.String(), "hello world")
	}

	end := firstEvent(events, "turn_end")
	if stop, _ := end["stop"].(string); stop != "end" {
		t.Errorf("turn_end stop = %q, want \"end\" (event: %v)", stop, end)
	}
	if _, hasErr := end["error"]; hasErr {
		t.Errorf("turn_end carries error: %v", end)
	}

	// Canonical wire shapes (core.WireEvent — shared with RPC and the
	// SDK): messages nest under "message", usage under "usage".
	am := firstEvent(events, "assistant_message")
	msg, _ := am["message"].(map[string]any)
	if msg == nil || msg["role"] != "assistant" {
		t.Errorf("assistant_message not in canonical nested shape: %v", am)
	}
	if usage := firstEvent(events, "usage"); usage != nil {
		u, _ := usage["usage"].(map[string]any)
		if u == nil || u["input"] == nil {
			t.Errorf("usage event not in canonical nested shape: %v", usage)
		}
	}
}

func TestJSONMode_WriteToolRoundTrip(t *testing.T) {
	const fileContent = "hi from e2e\n"
	args, err := json.Marshal(map[string]string{"path": "hello.txt", "content": fileContent})
	if err != nil {
		t.Fatal(err)
	}

	fp := newFakeProvider(t,
		// Turn 1: the model asks to write a file.
		sseScript{
			chunks: []map[string]any{
				toolCallChunk("call_1", "write", string(args)),
				finishChunk("tool_calls", 20, 10),
			},
			sendDone: true,
		},
		// Turn 2: after seeing the tool result, the model wraps up.
		sseScript{
			chunks:   []map[string]any{textChunk("done"), finishChunk("stop", 35, 2)},
			sendDone: true,
		},
	)
	ws := t.TempDir()

	res := runTerva(t, fp.url(), ws, "--json", "create hello.txt")

	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}

	// The side effect: the tool actually ran against the workspace.
	got, err := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if err != nil {
		t.Fatalf("expected tool side effect missing: %v", err)
	}
	if string(got) != fileContent {
		t.Errorf("hello.txt content = %q, want %q", got, fileContent)
	}

	events := parseEvents(t, res.stdout)
	call := firstEvent(events, "tool_call")
	if call == nil {
		t.Fatalf("no tool_call event; types: %v", eventTypes(events))
	}
	if call["name"] != "write" {
		t.Errorf("tool_call name = %v, want write", call["name"])
	}
	result := firstEvent(events, "tool_result")
	if result == nil {
		t.Fatalf("no tool_result event; types: %v", eventTypes(events))
	}
	if isErr, _ := result["is_error"].(bool); isErr {
		t.Errorf("tool_result reports error: %v", result)
	}
	end := lastEvent(events, "turn_end")
	if stop, _ := end["stop"].(string); stop != "end" {
		t.Errorf("final turn_end stop = %q, want \"end\"", stop)
	}

	// The wire round-trip: request 2 must carry the assistant tool
	// call and a role:"tool" result tied to the same call id.
	reqs := fp.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(reqs))
	}
	var sawAssistantCall, sawToolResult bool
	for _, m := range reqs[1].Messages {
		switch m["role"] {
		case "assistant":
			if calls, ok := m["tool_calls"].([]any); ok && len(calls) > 0 {
				sawAssistantCall = true
			}
		case "tool":
			if m["tool_call_id"] == "call_1" {
				sawToolResult = true
			}
		}
	}
	if !sawAssistantCall {
		t.Errorf("second request lacks assistant tool_calls message: %v", reqs[1].Messages)
	}
	if !sawToolResult {
		t.Errorf("second request lacks role:tool result for call_1: %v", reqs[1].Messages)
	}
}

func TestJSONMode_MidStreamDeathSurfacesError(t *testing.T) {
	// No [DONE]: the connection drops after some text. Before the
	// stream-death fix this was reported as a clean stop; it must
	// surface as an error and a nonzero exit.
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("partial answ")},
		sendDone: false,
	})
	ws := t.TempDir()

	res := runTerva(t, fp.url(), ws, "--json", "say something")

	if res.exitCode == 0 {
		t.Errorf("mid-stream death exited 0\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	events := parseEvents(t, res.stdout)
	end := lastEvent(events, "turn_end")
	endHasErr := false
	if end != nil {
		_, endHasErr = end["error"]
	}
	if !endHasErr && !hasType(events, "error") {
		t.Errorf("no error surfaced after mid-stream death; types: %v", eventTypes(events))
	}
}

func TestJSONMode_RateLimitRetriesAndSucceeds(t *testing.T) {
	// A 429 with Retry-After must be retried (after honoring the
	// stated wait) and the turn must then succeed — the binary-level
	// proof of the typed ProviderError retry path. Under the old
	// substring classification a 429 worked too, but a Retry-After
	// header was ignored entirely.
	fp := newFakeProvider(t,
		sseScript{httpStatus: 429, retryAfter: "1"},
		sseScript{
			chunks:   []map[string]any{textChunk("recovered"), finishChunk("stop", 9, 2)},
			sendDone: true,
		},
	)
	ws := t.TempDir()

	start := time.Now()
	res := runTerva(t, fp.url(), ws, "--json", "say hi")
	elapsed := time.Since(start)

	if res.exitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	if got := len(fp.requests()); got != 2 {
		t.Fatalf("provider saw %d requests, want 2 (429 then retry)", got)
	}
	// The Retry-After: 1 must actually be honored before the retry.
	if elapsed < time.Second {
		t.Errorf("retry happened after %v; expected >= 1s (Retry-After honored)", elapsed)
	}

	events := parseEvents(t, res.stdout)
	end := lastEvent(events, "turn_end")
	if stop, _ := end["stop"].(string); stop != "end" {
		t.Errorf("final turn_end stop = %q, want \"end\"", stop)
	}
	var text strings.Builder
	for _, ev := range events {
		if ev["type"] == "text_delta" {
			d, _ := ev["delta"].(string)
			text.WriteString(d)
		}
	}
	if text.String() != "recovered" {
		t.Errorf("reassembled text = %q, want %q", text.String(), "recovered")
	}
}
