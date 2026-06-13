package swarm

import (
	"encoding/json"
	"strings"
	"testing"
)

// A multi-line user prompt survives the inbox round-trip: the JSON
// envelope keeps it on one wire line, and ParseInboxLine recovers the
// body verbatim. This is the bug the old "user <text>\n" framing had —
// it split the prompt and dropped everything after the first line.
func TestInboxFramingNewlineSafe(t *testing.T) {
	body := "line one\nline two\nline three with } brace and \"quote\""
	m := InboxMsg{Kind: "user", Text: body}
	line, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "\n") {
		t.Fatalf("encoded frame contains a raw newline: %q", line)
	}
	kind, text := ParseInboxLine(string(line))
	if kind != "user" || text != body {
		t.Fatalf("round-trip = (%q, %q), want (user, %q)", kind, text, body)
	}
}

// ParseInboxLine accepts both the JSON envelope and the legacy text
// control lines (so an older parent, or the transport tests, still
// drive a new child).
func TestParseInboxLineForms(t *testing.T) {
	cases := []struct {
		line, kind, text string
	}{
		{`{"kind":"user","text":"hi"}`, "user", "hi"},
		{`{"kind":"shutdown"}`, "shutdown", ""},
		{`{"kind":"cancel"}`, "cancel", ""},
		{"user hello world", "user", "hello world"},
		{"shutdown", "shutdown", ""},
		{"cancel", "cancel", ""},
		{"garbage line", "", ""},
		{`{"kind":""}`, "", ""}, // empty kind is not a valid envelope
	}
	for _, c := range cases {
		kind, text := ParseInboxLine(c.line)
		if kind != c.kind || text != c.text {
			t.Errorf("ParseInboxLine(%q) = (%q, %q), want (%q, %q)", c.line, kind, text, c.kind, c.text)
		}
	}
}

// taskLevelTurnEnd recognises the explicit task_end event and the
// legacy turn_end{step}, but never a core per-turn turn_end{stop}.
func TestTaskLevelTurnEndDiscrimination(t *testing.T) {
	taskEnd := Event{Type: "task_end", Data: map[string]any{"step": float64(3), "error": "boom"}}
	if step, errMsg, ok := taskLevelTurnEnd(taskEnd); !ok || step != 3 || errMsg != "boom" {
		t.Errorf("task_end = (%d, %q, %v), want (3, boom, true)", step, errMsg, ok)
	}

	legacy := Event{Type: "turn_end", Data: map[string]any{"step": float64(1)}}
	if _, _, ok := taskLevelTurnEnd(legacy); !ok {
		t.Error("legacy turn_end{step} should be recognised for replay")
	}

	perTurn := Event{Type: "turn_end", Data: map[string]any{"stop": "end"}}
	if _, _, ok := taskLevelTurnEnd(perTurn); ok {
		t.Error("core per-turn turn_end{stop} must NOT count as task completion")
	}

	other := Event{Type: "tool_call", Data: map[string]any{}}
	if _, _, ok := taskLevelTurnEnd(other); ok {
		t.Error("non-turn-end event must not count")
	}
}
