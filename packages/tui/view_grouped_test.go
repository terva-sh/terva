package tui

// Tests for ToolDisplayGrouped: the mode that collapses a consecutive
// run of tool calls into ONE muted disclosure line ("▸ N tool calls …"),
// mirroring the web UI's grouped transcript. The correctness-sensitive
// parts are the run-detection state machine and the /jump anchor
// remapping — every message a run swallows must still anchor to the
// visible summary row.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func toolCall(id, name, args string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)}},
	}
}

func toolResult(id string, isErr bool, body string) provider.ToolResultBlock {
	return provider.ToolResultBlock{
		CallID:  id,
		IsError: isErr,
		Content: []provider.Content{provider.TextBlock{Text: body}},
	}
}

func userMsg(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func assistantMsg(text string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func TestToolDisplayGroupedString(t *testing.T) {
	if got := ToolDisplayGrouped.String(); got != "grouped" {
		t.Fatalf("ToolDisplayGrouped.String() = %q, want %q", got, "grouped")
	}
}

// A run of three calls (bash, bash, read) collapses to one summary line
// carrying the shared name summary and the call count; no box borders,
// no result bodies, and surrounding prose survives.
func TestGroupedCollapsesRun(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("do the thing"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "t1", Name: "bash", Arguments: json.RawMessage(`{"command":"go build"}`)},
			provider.ToolCallBlock{ID: "t2", Name: "bash", Arguments: json.RawMessage(`{"command":"go test"}`)},
			provider.ToolCallBlock{ID: "t3", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			toolResult("t1", false, "ok"),
			toolResult("t2", false, "PASS"),
			toolResult("t3", false, "package main"),
		}},
		assistantMsg("all done."),
	}}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "3 tool calls") {
		t.Fatalf("grouped line should count the run:\n%s", plain)
	}
	if !strings.Contains(plain, "bash ×2, read") {
		t.Fatalf("grouped line should carry the shared summary:\n%s", plain)
	}
	if strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("grouped display must not render boxes:\n%s", plain)
	}
	if strings.Contains(plain, "PASS") || strings.Contains(plain, "package main") {
		t.Fatalf("grouped display must not render result bodies:\n%s", plain)
	}
	if !strings.Contains(plain, "do the thing") || !strings.Contains(plain, "all done.") {
		t.Fatalf("prose around the run must survive:\n%s", plain)
	}
	if strings.Contains(plain, "failed") {
		t.Fatalf("a clean run must not mention failures:\n%s", plain)
	}
}

// Every message a run collapses (the transparent tool-call assistant
// message AND the results message) must anchor to the summary row so
// /jump lands on a visible line; the user prompt and the trailing reply
// keep their own exact rows.
func TestGroupedAnchorsRemapToSummary(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		toolCall("t1", "bash", `{"command":"ls"}`),
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "a\nb")}},
		assistantMsg("done."),
	}}

	chat, anchors := v.BuildWithAnchors(80)
	if len(anchors) != 4 {
		t.Fatalf("want one anchor per message, got %d", len(anchors))
	}
	for i, a := range anchors {
		if a.MessageIdx != i {
			t.Fatalf("anchor %d has MessageIdx %d, want %d", i, a.MessageIdx, i)
		}
	}
	// The two collapsed messages (idx 1 and 2) share the summary row.
	if anchors[1].Row != anchors[2].Row {
		t.Fatalf("collapsed run messages must share the summary row: %d vs %d", anchors[1].Row, anchors[2].Row)
	}
	// Rows are strictly ordered: user < summary < reply.
	if !(anchors[0].Row < anchors[1].Row && anchors[1].Row < anchors[3].Row) {
		t.Fatalf("anchor rows out of order: %+v", anchors)
	}
	// The summary row actually holds the summary line.
	if got := stripANSI(chat[anchors[1].Row]); !strings.Contains(got, "1 tool call") || !strings.Contains(got, "bash") {
		t.Fatalf("summary row %d = %q, want the grouped line", anchors[1].Row, got)
	}
	// The reply anchor lands on the reply text.
	if got := stripANSI(chat[anchors[3].Row]); !strings.Contains(got, "done.") {
		t.Fatalf("reply row %d = %q, want the assistant reply", anchors[3].Row, got)
	}
}

// A single-line "1 tool call" must not be pluralised.
func TestGroupedSingleCallSingular(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		toolCall("t1", "bash", `{"command":"ls"}`),
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "x")}},
	}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if strings.Contains(plain, "1 tool calls") {
		t.Fatalf("singular count must not be pluralised:\n%s", plain)
	}
	if !strings.Contains(plain, "1 tool call") {
		t.Fatalf("expected a singular grouped count:\n%s", plain)
	}
}

// Failures are surfaced by the group head's "· N failed" count, not by
// per-box error rendering.
func TestGroupedShowsFailures(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "t1", Name: "bash", Arguments: json.RawMessage(`{"command":"go test"}`)},
			provider.ToolCallBlock{ID: "t2", Name: "edit", Arguments: json.RawMessage(`{"path":"x.go"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			toolResult("t1", true, "FAIL"),
			toolResult("t2", false, "edited"),
		}},
	}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "2 tool calls") {
		t.Fatalf("want the run count:\n%s", plain)
	}
	if !strings.Contains(plain, "1 failed") {
		t.Fatalf("grouped head must surface the failure count:\n%s", plain)
	}
	if strings.Contains(plain, "FAIL") {
		t.Fatalf("grouped display must not render the error body:\n%s", plain)
	}
}

// Assistant prose between two bursts of tool calls breaks the run: each
// burst collapses to its own summary line and the prose survives between
// them.
func TestGroupedRunInterruptedByText(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "let me check"},
			provider.ToolCallBlock{ID: "t1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "ok")}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "now the file"},
			provider.ToolCallBlock{ID: "t2", Name: "read", Arguments: json.RawMessage(`{"path":"m.go"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t2", false, "code")}},
		assistantMsg("done."),
	}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	// Two separate runs → two summary lines, each a single call.
	if n := strings.Count(plain, "1 tool call"); n != 2 {
		t.Fatalf("expected two single-call summary lines, got %d:\n%s", n, plain)
	}
	for _, want := range []string{"let me check", "now the file", "done."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("prose %q lost around the runs:\n%s", want, plain)
		}
	}
}

// A run at the very tail (no trailing reply) still collapses to a summary.
func TestGroupedRunAtTail(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		toolCall("t1", "bash", `{"command":"ls"}`),
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "ok")}},
	}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "1 tool call") || !strings.Contains(plain, "bash") {
		t.Fatalf("tail run must still summarise:\n%s", plain)
	}
	if strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("tail run must not render a box:\n%s", plain)
	}
}

// ctrl+o (ExpandAll) is the recovery hatch: it overrides grouped mode and
// restores full boxes with result bodies, exactly like it does for
// minimal/hidden.
func TestGroupedExpandAllRestoresBoxes(t *testing.T) {
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, ExpandAll: true, Messages: []provider.Message{
		userMsg("go"),
		toolCall("t1", "bash", `{"command":"go test"}`),
		{Role: provider.RoleTool, Content: []provider.Content{toolResult("t1", false, "PASS\nok")}},
	}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "│") || !strings.Contains(plain, "PASS") {
		t.Fatalf("ExpandAll must restore full boxes over grouped:\n%s", plain)
	}
	if strings.Contains(plain, "tool call") {
		t.Fatalf("ExpandAll must not leave a grouped summary line:\n%s", plain)
	}
}

// An in-flight call has no run yet: grouped mode shows it as a minimal
// live line so the user sees progress; it folds into a run summary once
// its result reaches the transcript.
func TestGroupedLiveOverlayMinimalLine(t *testing.T) {
	args := json.RawMessage(`{"command":"sleep 5"}`)
	v := View{Theme: Dark, ToolDisplay: ToolDisplayGrouped, Messages: []provider.Message{
		userMsg("go"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "t1", Name: "bash", Arguments: args},
		}},
	}, ToolCalls: []ToolCallView{{ID: "t1", Name: "bash", Args: ShortArgs("bash", args)}}}
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "· bash sleep 5 — running…") {
		t.Fatalf("grouped live overlay should show a minimal running line:\n%s", plain)
	}
	if strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("grouped live overlay must not render a box:\n%s", plain)
	}
}
