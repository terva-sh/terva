package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// echoTool is a minimal core.Tool that echoes its "text" arg back.
type echoTool struct{}

func (echoTool) Name() string            { return "echo" }
func (echoTool) Description() string     { return "echo" }
func (echoTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (echoTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &a)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "echo:" + a.Text}}}, nil
}

type fakeHostToolSource struct{ ext bool }

func (f fakeHostToolSource) HasTool(string) bool { return f.ext }

func TestHostToolDispatcher(t *testing.T) {
	ag := &core.Agent{Tools: core.Registry{"echo": echoTool{}}}
	args := json.RawMessage(`{"text":"hi"}`)

	// Happy path: a nil gate allows, the tool runs, text comes back.
	d := buildHostToolDispatcher(ag, nil, fakeHostToolSource{ext: false})
	content, isErr := d(context.Background(), "ext", "echo", args, false)
	if isErr {
		t.Fatalf("unexpected error: %v", content)
	}
	if len(content) != 1 || content[0].Text != "echo:hi" {
		t.Errorf("content = %+v, want one text block \"echo:hi\"", content)
	}

	// Recursion guard: an extension-owned tool is refused.
	d2 := buildHostToolDispatcher(ag, nil, fakeHostToolSource{ext: true})
	content, isErr = d2(context.Background(), "ext", "echo", args, false)
	if !isErr || !strings.Contains(content[0].Text, "extension tool") {
		t.Errorf("extension tool should be refused, got isErr=%v %+v", isErr, content)
	}

	// Unknown tool errors cleanly.
	content, isErr = d(context.Background(), "ext", "nope", args, false)
	if !isErr || !strings.Contains(content[0].Text, "no such host tool") {
		t.Errorf("unknown tool should error, got isErr=%v %+v", isErr, content)
	}
}

func TestHostToolDispatcherGateDenies(t *testing.T) {
	ag := &core.Agent{Tools: core.Registry{"echo": echoTool{}}}
	// A policy gate in plan mode denies a non-read-only tool.
	gate := core.NewPolicyGate(&core.PermissionPolicy{
		Mode:     core.ApprovalPlan,
		ReadOnly: core.NewReadOnlySet("read"),
	}, nil)
	d := buildHostToolDispatcher(ag, gate, fakeHostToolSource{ext: false})
	content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"x"}`), false)
	if !isErr || !strings.Contains(content[0].Text, "denied") {
		t.Errorf("gate should deny the call in plan mode, got isErr=%v %+v", isErr, content)
	}
}
