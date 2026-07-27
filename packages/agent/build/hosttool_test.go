package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
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

// Both gate verdicts reached through the host_tool_call door land in the
// audit log stamped via=host_tool_call — this door checks the gate outside
// the BeforeToolExecute ladder, whose deferred audit never sees it.
func TestHostToolDispatcherAudits(t *testing.T) {
	home := testsupport.TempDir(t)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	ag := &core.Agent{Tools: core.Registry{"echo": echoTool{}}}
	// Allowed by a nil gate (the yolo spelling): one allow line, empty mode.
	d := buildHostToolDispatcher(ag, nil, fakeHostToolSource{})
	if _, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"hi"}`), false); isErr {
		t.Fatal("expected the nil-gate call to pass")
	}
	// Denied by a plan-mode gate: one deny line carrying the reason.
	gate := core.NewPolicyGate(&core.PermissionPolicy{
		Mode:     core.ApprovalPlan,
		ReadOnly: core.NewReadOnlySet("read"),
	}, nil)
	d2 := buildHostToolDispatcher(ag, gate, fakeHostToolSource{})
	if _, isErr := d2(context.Background(), "ext", "echo", json.RawMessage(`{"text":"x"}`), false); !isErr {
		t.Fatal("expected the plan-mode call to be denied")
	}
	auditSink.Close()

	recs := readAuditLines(t, home)
	if len(recs) != 2 {
		t.Fatalf("want 2 audit records, got %d", len(recs))
	}
	if recs[0].Via != auditViaHostToolCall || recs[0].Decision != "allow" || recs[0].Tool != "echo" || recs[0].Mode != "" {
		t.Errorf("allow record wrong: %+v", recs[0])
	}
	if recs[1].Via != auditViaHostToolCall || recs[1].Decision != "deny" || recs[1].Reason == "" {
		t.Errorf("deny record wrong: %+v", recs[1])
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
