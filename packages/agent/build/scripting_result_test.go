//go:build terva_scripting

package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/provider"
)

type structuredInspectionTool struct{ calls int }

func (t *structuredInspectionTool) Name() string        { return "session_inspect" }
func (t *structuredInspectionTool) Description() string { return "fixture" }
func (t *structuredInspectionTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t *structuredInspectionTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	t.calls++
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "display"}}, StructuredContent: json.RawMessage(`{"count":7}`)}, nil
}

func TestScriptingStructuredResultUsesPermissionGate(t *testing.T) {
	ce := &tools.CodeExecutionTool{}
	target := &structuredInspectionTool{}
	ag := coretest.NewAgent(nil, "fake", "", nil)
	reg := core.Registry{"code_execution": ce, "session_inspect": target}
	ag.SetToolsWithReadOnly(reg, core.NewReadOnlySet("code_execution", "session_inspect"))
	gate := permission.NewPolicyGate(&permission.PermissionPolicy{Mode: permission.ApprovalPlan}, nil)
	WireHostToolDispatcher(ag, nil, gate)
	args := json.RawMessage(`{"script":"print(callResult(\"session_inspect\",{}).structuredContent.count);"}`)
	res, err := ce.Execute(context.Background(), args, nil)
	if err != nil || res.IsError || target.calls != 1 || !strings.Contains(textFromResult(res), "7") {
		t.Fatalf("allowed structured call failed: %+v, %v", res, err)
	}
	ag.SetToolsWithReadOnly(reg, core.NewReadOnlySet("code_execution"))
	WireHostToolDispatcher(ag, nil, gate)
	res, err = ce.Execute(context.Background(), args, nil)
	if err != nil || !res.IsError || target.calls != 1 || !strings.Contains(textFromResult(res), "denied") {
		t.Fatalf("structured call bypassed plan gate: %+v, %v", res, err)
	}
}
