//go:build terva_scripting

package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestScriptingClassification(t *testing.T) {
	if !ScriptingSupported() {
		t.Fatal("ScriptingSupported must be true under terva_scripting")
	}
	if !readOnlyTools["code_execution"] {
		t.Fatal("code_execution must classify read-only (its binding set is read-only)")
	}
	if !BuiltinTools["code_execution"] {
		t.Fatal("code_execution must classify as a first-party builtin")
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
