package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestLiveToolOverlayRemainsAfterAssistantToolUse(t *testing.T) {
	args := json.RawMessage(`{"command":"sleep 1"}`)
	v := View{
		Theme: Dark,
		Messages: []provider.Message{
			{
				Role: provider.RoleAssistant,
				Content: []provider.Content{
					provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args},
				},
			},
		},
		ToolCalls: []ToolCallView{
			{ID: "toolu_1", Name: "bash", Args: ShortArgs("bash", args), Done: false},
		},
	}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "bash sleep 1") {
		t.Fatalf("live tool overlay disappeared after assistant tool_use was appended:\n%s", plain)
	}
}

func TestLiveToolOverlayKeepsWritePreviewAfterArgsEnd(t *testing.T) {
	args := json.RawMessage(`{"path":"/tmp/sample.ts","content":"export const n = 1\n"}`)
	v := View{
		Theme: Dark,
		Messages: []provider.Message{
			{
				Role: provider.RoleAssistant,
				Content: []provider.Content{
					provider.ToolCallBlock{ID: "toolu_1", Name: "write", Arguments: args},
				},
			},
		},
		ToolCalls: []ToolCallView{
			{
				ID:         "toolu_1",
				Name:       "write",
				Args:       ShortArgs("write", args),
				Streaming:  false,
				RawJSONBuf: string(args),
				LivePath:   "/tmp/sample.ts",
			},
		},
	}

	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "export const n = 1") {
		t.Fatalf("write preview collapsed after tool args ended but before tool_result arrived:\n%s", plain)
	}
}

func TestLiveToolOverlayHidesAfterToolResult(t *testing.T) {
	args := json.RawMessage(`{"command":"sleep 1"}`)
	v := View{
		Theme: Dark,
		Messages: []provider.Message{
			{
				Role: provider.RoleAssistant,
				Content: []provider.Content{
					provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args},
				},
			},
			{
				Role: provider.RoleTool,
				Content: []provider.Content{
					provider.ToolResultBlock{
						CallID:  "toolu_1",
						Content: []provider.Content{provider.TextBlock{Text: "done"}},
					},
				},
			},
		},
		ToolCalls: []ToolCallView{
			{ID: "toolu_1", Name: "bash", Args: ShortArgs("bash", args), Result: "done", Done: true},
		},
	}

	plain := stripANSI(strings.Join(v.BuildLive(80), "\n"))
	if strings.Contains(plain, "bash sleep 1") {
		t.Fatalf("live tool overlay still rendered after tool_result was appended:\n%s", plain)
	}
}

// A live tool box must not shrink mid-stream: once a streaming preview has
// reached some height, a shorter follow-up preview for the SAME call id
// (e.g. between edit 1 and the start of edit 2) is padded up to the
// per-call high-water mark so the editor/status band below never jumps.
func TestLiveToolBoxDoesNotShrinkMidStream(t *testing.T) {
	mk := func(args json.RawMessage) ToolCallView {
		return ToolCallView{
			ID:         "toolu_1",
			Name:       "write",
			Args:       ShortArgs("write", args),
			RawJSONBuf: string(args),
			LivePath:   "/tmp/x.ts",
		}
	}
	long := json.RawMessage(`{"path":"/tmp/x.ts","content":"a\nb\nc\nd\ne\nf\n"}`)
	short := json.RawMessage(`{"path":"/tmp/x.ts","content":"a\n"}`)

	v := View{Theme: Dark, liveBodyHigh: make(map[string]int)}
	tall := len(v.renderToolCall(mk(long), 80))
	if tall == 0 {
		t.Fatal("expected a non-empty live tool box")
	}
	shrunk := len(v.renderToolCall(mk(short), 80))
	if shrunk != tall {
		t.Fatalf("live tool box shrank mid-stream: %d rows, then %d after the body shrank; "+
			"per-call high-water padding should hold the height steady", tall, shrunk)
	}
}
