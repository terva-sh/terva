package core

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/provider"
)

// recordingTool captures the args it was invoked with so the test
// can verify the interceptor-rewritten args reached execution.
type recordingTool struct {
	lastArgs json.RawMessage
}

func (r *recordingTool) Name() string            { return "echo" }
func (r *recordingTool) Description() string     { return "echoes" }
func (r *recordingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (r *recordingTool) Execute(_ context.Context, args json.RawMessage, _ func(string)) (ToolResult, error) {
	r.lastArgs = append(json.RawMessage(nil), args...)
	return ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}, nil
}

// TestGateModifiesArgs verifies that a non-nil
// modifiedArgs returned from the gate is what the tool
// actually sees.
func TestGateModifiesArgs(t *testing.T) {
	rec := &recordingTool{}
	reg := Registry{"echo": rec}
	a := newTestAgent(nil, "test", "", reg)

	newArgs := json.RawMessage(`{"command":"echo GUARDED: ls"}`)
	a.gate = GateFunc(func(_ context.Context, call provider.ToolCallBlock, _ Tool) (bool, string, json.RawMessage) {
		return true, "", newArgs
	})

	ctx := context.Background()
	res := a.runOneTool(ctx, provider.ToolCallBlock{
		ID:        "T1",
		Name:      "echo",
		Arguments: json.RawMessage(`{"command":"ls"}`),
	}, a.tools, func(AgentEvent) {})
	if res.IsError {
		t.Fatalf("unexpected error result: %v", res.Content)
	}
	if string(rec.lastArgs) != string(newArgs) {
		t.Errorf("tool saw %s, want %s", string(rec.lastArgs), string(newArgs))
	}
}

// TestGateInvalidJSONIgnored verifies that returning
// malformed JSON as modifiedArgs leaves the original args intact
// (safety: a buggy interceptor can't corrupt the call).
func TestGateInvalidJSONIgnored(t *testing.T) {
	rec := &recordingTool{}
	reg := Registry{"echo": rec}
	a := newTestAgent(nil, "test", "", reg)

	a.gate = GateFunc(func(_ context.Context, call provider.ToolCallBlock, _ Tool) (bool, string, json.RawMessage) {
		return true, "", json.RawMessage(`{not json`)
	})

	ctx := context.Background()
	orig := json.RawMessage(`{"command":"ls"}`)
	a.runOneTool(ctx, provider.ToolCallBlock{
		ID:        "T1",
		Name:      "echo",
		Arguments: orig,
	}, a.tools, func(AgentEvent) {})
	if string(rec.lastArgs) != string(orig) {
		t.Errorf("tool saw %s, want original %s", string(rec.lastArgs), string(orig))
	}
}

// TestGateBlockSurfacesReason verifies a refusal from
// the interceptor returns an error ToolResult with the reason text.
func TestGateBlockSurfacesReason(t *testing.T) {
	rec := &recordingTool{}
	reg := Registry{"echo": rec}
	a := newTestAgent(nil, "test", "", reg)

	a.gate = GateFunc(func(_ context.Context, call provider.ToolCallBlock, _ Tool) (bool, string, json.RawMessage) {
		return false, "nope", nil
	})

	ctx := context.Background()
	res := a.runOneTool(ctx, provider.ToolCallBlock{
		ID:        "T1",
		Name:      "echo",
		Arguments: json.RawMessage(`{"command":"ls"}`),
	}, a.tools, func(AgentEvent) {})
	if !res.IsError {
		t.Fatal("want error result, got success")
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tb, ok := res.Content[0].(provider.TextBlock)
	if !ok || tb.Text != "nope" {
		t.Errorf("want reason 'nope', got %v", res.Content[0])
	}
	if rec.lastArgs != nil {
		t.Error("tool ran despite block")
	}
}
