package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
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
	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"echo": echoTool{}})
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

func TestHostToolDispatchUsesCurrentGeneration(t *testing.T) {
	ag := coretest.NewAgent(nil, "fake", "", nil)
	gate := permission.NewPolicyGate(&permission.PermissionPolicy{Mode: permission.ApprovalWorkspace, ReadOnly: core.NewReadOnlySet("echo")}, nil)
	dispatch := buildHostToolDispatcher(ag, gate, fakeHostToolSource{})
	ag.SetToolsWithReadOnly(core.Registry{"echo": echoTool{}}, core.NewReadOnlySet("echo"))
	if _, failed := dispatch(context.Background(), "ext", "echo", nil, false); failed {
		t.Fatal("read-only host call failed")
	}
	ag.SetToolsWithReadOnly(core.Registry{"echo": echoTool{}}, core.NewReadOnlySet())
	if _, failed := dispatch(context.Background(), "ext", "echo", nil, false); !failed {
		t.Fatal("host call retained old read-only authority")
	}
}

// Both gate verdicts reached through the host_tool_call door land in the
// audit log stamped via=host_tool_call — this door checks the gate outside
// the tool-call ladder, whose deferred audit never sees it.
func TestHostToolDispatcherAudits(t *testing.T) {
	home := testsupport.TempDir(t)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"echo": echoTool{}})
	// Allowed by a nil gate (the yolo spelling): one allow line, empty mode.
	d := buildHostToolDispatcher(ag, nil, fakeHostToolSource{})
	if _, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"hi"}`), false); isErr {
		t.Fatal("expected the nil-gate call to pass")
	}
	// Denied by a plan-mode gate: one deny line carrying the reason.
	gate := permission.NewPolicyGate(&permission.PermissionPolicy{
		Mode:     permission.ApprovalPlan,
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
	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"echo": echoTool{}})
	// A policy gate in plan mode denies a non-read-only tool.
	gate := permission.NewPolicyGate(&permission.PermissionPolicy{
		Mode:     permission.ApprovalPlan,
		ReadOnly: core.NewReadOnlySet("read"),
	}, nil)
	d := buildHostToolDispatcher(ag, gate, fakeHostToolSource{ext: false})
	content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"x"}`), false)
	if !isErr || !strings.Contains(content[0].Text, "denied") {
		t.Errorf("gate should deny the call in plan mode, got isErr=%v %+v", isErr, content)
	}
}

// 🚨 A host's guard binds this door too. An extension that runs a host tool
// for a model meets the refusal that the model's own call meets, the tool
// does not run, and the audit log holds the refusal.
func TestHostToolDispatcherAsksTheHostsGuards(t *testing.T) {
	home := testsupport.TempDir(t)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"echo": echoTool{}})
	guard := func(_ context.Context, tool string, args json.RawMessage) string {
		if strings.Contains(string(args), "merge") {
			return "the guard refuses a merge"
		}
		return ""
	}
	// A nil gate allows every call, so only the guard can refuse here.
	d := buildHostToolDispatcher(ag, nil, fakeHostToolSource{}, nil, CallGuard(guard))
	content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"merge it"}`), false)
	if !isErr || !strings.Contains(content[0].Text, "the guard refuses a merge") {
		t.Fatalf("the guard's refusal must reach the extension, got isErr=%v %+v", isErr, content)
	}
	if content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"hi"}`), false); isErr {
		t.Fatalf("a call that the guard has no word on must run: %+v", content)
	}
	auditSink.Close()
	recs := readAuditLines(t, home)
	if len(recs) != 2 || recs[0].Decision != "deny" || recs[0].Reason != "the guard refuses a merge" || recs[1].Decision != "allow" {
		t.Fatalf("the audit log must hold the refusal and then the call: %+v", recs)
	}
}

// 🚨 An extension that runs a host tool for a model meets the host's rung
// behind the gate too. A count that the host keeps there must count the
// extension's call as it counts the model's own, and the audit log holds the
// last word on the call.
func TestHostToolDispatcherRunsTheRungBehindTheGate(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"echo": echoTool{}})
	left := 1
	var seen []string
	after := AfterAllow(func(_ context.Context, tool string, args json.RawMessage) string {
		seen = append(seen, tool+" "+string(args))
		if left == 0 {
			return "no request is left"
		}
		left--
		return ""
	})
	guard := CallGuard(func(_ context.Context, _ string, args json.RawMessage) string {
		if strings.Contains(string(args), "merge") {
			return "the guard refuses a merge"
		}
		return ""
	})
	d := buildHostToolDispatcher(ag, nil, fakeHostToolSource{}, after, guard)
	// A call that the guard refused does not reach the rung behind.
	if _, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"merge it"}`), false); !isErr || len(seen) != 0 {
		t.Fatalf("the rung behind must not run for a call that a guard refused: isErr=%v seen=%q", isErr, seen)
	}
	if content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"one"}`), false); isErr || len(seen) != 1 {
		t.Fatalf("the first call must run and reach the rung one time: %+v seen=%q", content, seen)
	}
	content, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"two"}`), false)
	if !isErr || !strings.Contains(content[0].Text, "no request is left") || len(seen) != 2 {
		t.Fatalf("the refusal of the rung behind must reach the extension: isErr=%v %+v seen=%q", isErr, content, seen)
	}
	// 🚨 A call that the gate refused does not reach it either. Plan mode
	// refuses a tool that is not read-only.
	plan := permission.NewPolicyGate(&permission.PermissionPolicy{Mode: permission.ApprovalPlan, ReadOnly: core.NewReadOnlySet("read")}, nil)
	left = 1
	d = buildHostToolDispatcher(ag, plan, fakeHostToolSource{}, after)
	if _, isErr := d(context.Background(), "ext", "echo", json.RawMessage(`{"text":"three"}`), false); !isErr || len(seen) != 2 || left != 1 {
		t.Fatalf("the rung behind must not run for a call that the gate refused: isErr=%v seen=%q left=%d", isErr, seen, left)
	}
	auditSink.Close()
	recs := readAuditLines(t, home)
	if len(recs) != 4 || recs[0].Decision != "deny" || recs[1].Decision != "allow" || recs[2].Decision != "deny" || recs[2].Reason != "no request is left" || recs[3].Decision != "deny" {
		t.Fatalf("the audit log must hold the last word on each call: %+v", recs)
	}
}

// 🚨 The bounds of the shell parser bind this door with no guard of a host.
// An extension that runs bash for a model meets the refusal, and the gate's
// parser never reads the line.
func TestHostToolDispatcherRefusesABashCommandPastTheBounds(t *testing.T) {
	ag := coretest.NewAgent(nil, "fake", "", core.Registry{"bash": echoTool{}})
	gate := permission.NewPolicyGate(permissions.NewPolicy(permission.ApprovalYolo, nil), nil)
	d := buildHostToolDispatcher(ag, gate, fakeHostToolSource{})
	deep, err := json.Marshal(map[string]string{"command": strings.Repeat("(", 250000) + "true" + strings.Repeat(")", 250000)})
	if err != nil {
		t.Fatal(err)
	}
	content, isErr := d(context.Background(), "ext", "bash", deep, false)
	if !isErr || !strings.Contains(content[0].Text, "this long or this deep") {
		t.Fatalf("a deep line must be refused, got isErr=%v %+v", isErr, content)
	}
	if content, isErr := d(context.Background(), "ext", "bash", json.RawMessage(`{"command":"ls"}`), false); isErr {
		t.Fatalf("a short command must run: %+v", content)
	}
}
