//go:build terva_scripting

package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestScriptingClassification(t *testing.T) {
	if !ScriptingSupported() {
		t.Fatal("ScriptingSupported must be true under terva_scripting")
	}
	if !permissions.IsReadOnly("code_execution") {
		t.Fatal("code_execution must classify read-only (its binding set is read-only)")
	}
	if !permissions.IsBuiltin("code_execution") {
		t.Fatal("code_execution must classify as a first-party builtin")
	}
}

// The mutating tool must NEVER classify read-only. This is the one
// regression that would be invisible in normal use and catastrophic in
// plan mode: a single stray RegisterReadOnly in scripting_on.go would put
// a tool that writes files into a mode that promises not to. The final
// assertion checks the consequence rather than the flag, so the test still
// holds if the pruning rule itself is ever rewritten.
func TestMutatingScriptToolIsNotReadOnly(t *testing.T) {
	if permissions.IsReadOnly("code_execution_mutating") {
		t.Fatal("code_execution_mutating must NOT classify read-only — it writes files")
	}
	if !permissions.IsBuiltin("code_execution_mutating") {
		t.Fatal("code_execution_mutating must classify as a first-party builtin")
	}

	ws := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
	cm, ok := ws["code_execution_mutating"]
	if !ok {
		t.Fatal("code_execution_mutating missing from the workspace registry")
	}
	// Its own lazy group: activating read-only scripting must not also
	// hand over the tool that writes.
	if g := ToolGroup(cm); g == "scripting" || g == "" {
		t.Fatalf("group = %q, want a group of its own, distinct from scripting", g)
	}

	plan := BuildToolRegistry(Args{}, core.ApprovalPlan, testsupport.TempDir(t), nil, "", "", false, nil)
	if _, ok := plan["code_execution_mutating"]; ok {
		t.Fatal("code_execution_mutating must not enter a plan-mode registry: plan mode promises read-only")
	}
	// The read-only sibling still survives, so this is a real distinction
	// and not the prune eating both.
	if _, ok := plan["code_execution"]; !ok {
		t.Fatal("code_execution should still survive plan mode")
	}
}

func TestMutatingScriptToolHostCallWiring(t *testing.T) {
	cm := &tools.CodeExecutionMutatingTool{}
	ag := &core.Agent{Tools: core.Registry{
		"code_execution_mutating": cm,
		"echo":                    echoTool{},
	}}
	// The same seam every run mode calls: the mutating tool must be
	// late-bound too, or it fails closed at execute time and is useless.
	WireHostToolDispatcher(ag, nil, nil)
	if cm.HostCall == nil {
		t.Fatal("HostCall not late-bound for code_execution_mutating")
	}
	res, err := cm.HostCall(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("host call: %v", err)
	}
	if txt := textFromResult(res); txt != "echo:hi" {
		t.Fatalf("host call text = %q", txt)
	}
}

// A write issued from inside a script is gated by the same path as any
// other write, so a gate that denies it denies the script's call too.
func TestMutatingScriptToolWriteIsGated(t *testing.T) {
	cm := &tools.CodeExecutionMutatingTool{}
	ag := &core.Agent{Tools: core.Registry{
		"code_execution_mutating": cm,
		"echo":                    echoTool{},
	}}
	gate := core.NewPolicyGate(&core.PermissionPolicy{
		Mode:     core.ApprovalPlan,
		ReadOnly: core.NewReadOnlySet("read"),
	}, nil)
	WireHostToolDispatcher(ag, nil, gate)
	if _, err := cm.HostCall(context.Background(), "echo", json.RawMessage(`{"text":"x"}`)); err == nil ||
		!strings.Contains(err.Error(), "denied") {
		t.Fatalf("gate should deny a script-issued call in plan mode, err = %v", err)
	}
}

// Each script binding call through code_execution's HostCall lands in the
// audit log stamped via=code_execution — like ext host_tool_call, this door
// checks the gate outside the BeforeToolExecute ladder.
func TestScriptingHostCallAudits(t *testing.T) {
	home := testsupport.TempDir(t)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	ag := &core.Agent{Tools: core.Registry{
		"code_execution": &tools.CodeExecutionTool{},
		"echo":           echoTool{},
	}}
	wireScriptingHostCall(ag, nil) // nil gate = yolo spelling: allowed, empty mode
	ce := ag.Tools["code_execution"].(*tools.CodeExecutionTool)
	if ce.HostCall == nil {
		t.Fatal("HostCall not wired")
	}
	if _, err := ce.HostCall(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`)); err != nil {
		t.Fatalf("HostCall: %v", err)
	}
	auditSink.Close()

	recs := readAuditLines(t, home)
	if len(recs) != 1 || recs[0].Via != auditViaScriptBinding || recs[0].Tool != "echo" || recs[0].Decision != "allow" {
		t.Fatalf("want one allow record via %s for echo, got %+v", auditViaScriptBinding, recs)
	}
}

func TestScriptingRegistryAndPlanMode(t *testing.T) {
	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
	ce, ok := reg["code_execution"]
	if !ok {
		t.Fatal("code_execution missing from the workspace registry")
	}
	if g := ToolGroup(ce); g != "scripting" {
		t.Fatalf("group = %q, want scripting (lazy visibility)", g)
	}
	// Read-only classification means it survives the plan-mode prune.
	plan := BuildToolRegistry(Args{}, core.ApprovalPlan, testsupport.TempDir(t), nil, "", "", false, nil)
	if _, ok := plan["code_execution"]; !ok {
		t.Fatal("code_execution should survive plan mode while its bindings are read-only")
	}
}

func TestScriptingHostCallWiring(t *testing.T) {
	ce := &tools.CodeExecutionTool{}
	ag := &core.Agent{Tools: core.Registry{
		"code_execution": ce,
		"echo":           echoTool{},
	}}
	// The same seam every run mode calls; extensions absent is fine.
	WireHostToolDispatcher(ag, nil, nil)
	if ce.HostCall == nil {
		t.Fatal("HostCall not late-bound by WireHostToolDispatcher")
	}
	res, err := ce.HostCall(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("host call: %v", err)
	}
	if txt := textFromResult(res); txt != "echo:hi" {
		t.Fatalf("host call text = %q", txt)
	}
	if _, err := ce.HostCall(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), "no such host tool") {
		t.Fatalf("unknown tool err = %v", err)
	}
}

func TestScriptingHostCallGateDenies(t *testing.T) {
	ce := &tools.CodeExecutionTool{}
	ag := &core.Agent{Tools: core.Registry{
		"code_execution": ce,
		"echo":           echoTool{},
	}}
	gate := core.NewPolicyGate(&core.PermissionPolicy{
		Mode:     core.ApprovalPlan,
		ReadOnly: core.NewReadOnlySet("read"),
	}, nil)
	WireHostToolDispatcher(ag, nil, gate)
	_, err := ce.HostCall(context.Background(), "echo", json.RawMessage(`{"text":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("gate should deny echo in plan mode, err = %v", err)
	}
}

func textFromResult(res core.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
