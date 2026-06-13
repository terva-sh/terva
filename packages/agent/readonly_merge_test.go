package agent

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeToolSource is a minimal ExtensionToolSource for merge tests.
type fakeToolSource struct {
	infos []ExtensionToolInfo
}

func (f *fakeToolSource) Tools() []ExtensionToolInfo { return f.infos }
func (f *fakeToolSource) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	return &fakeMergedTool{name: info.Name}
}

type fakeMergedTool struct{ name string }

func (t *fakeMergedTool) Name() string            { return t.name }
func (t *fakeMergedTool) Description() string     { return "fake" }
func (t *fakeMergedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeMergedTool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (core.ToolResult, error) {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// TestPlanModeAdmitsReadOnlyExtensionTools pins the full read-only
// annotation contract: in plan mode, an extension/MCP tool that
// declares read_only joins the registry AND the policy's
// classification (so the gate allows it), while an unannotated tool
// stays invisible and would be refused by the gate backstop.
func TestPlanModeAdmitsReadOnlyExtensionTools(t *testing.T) {
	pol := &core.PermissionPolicy{
		Mode:      core.ApprovalPlan,
		ReadOnly:  builtinReadOnlySet(),
		EditTools: editTools,
	}
	gate := core.NewPolicyGate(pol, nil)

	r := &Resolved{
		ToolRegistry: core.Registry{},
		approvalMode: core.ApprovalPlan,
	}
	r.AdoptReadOnlySet(pol.ReadOnly)
	r.MergeExtensionTools(&fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "x", Name: "peek_things", Schema: []byte(`{}`), ReadOnly: true},
		{Extension: "x", Name: "mutate_things", Schema: []byte(`{}`)},
	}})

	if _, ok := r.ToolRegistry["peek_things"]; !ok {
		t.Fatal("read_only tool should join the plan-mode registry")
	}
	if _, ok := r.ToolRegistry["mutate_things"]; ok {
		t.Fatal("mutating tool must stay out of the plan-mode registry")
	}
	if ok, _, _ := gate.Check("peek_things", nil, ""); !ok {
		t.Error("gate should allow the admitted read-only tool in plan mode")
	}
	if ok, _, _ := gate.Check("mutate_things", nil, ""); ok {
		t.Error("gate backstop must still refuse the mutating tool")
	}
}

// TestNonPlanModeMergeMarksReadOnly ensures the classification also
// grows outside plan mode, so auto-edit can auto-allow annotated
// extension tools.
func TestNonPlanModeMergeMarksReadOnly(t *testing.T) {
	pol := &core.PermissionPolicy{
		Mode:      core.ApprovalAutoEdit,
		ReadOnly:  builtinReadOnlySet(),
		EditTools: editTools,
	}
	gate := core.NewPolicyGate(pol, nil)
	r := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalAutoEdit}
	r.AdoptReadOnlySet(pol.ReadOnly)
	r.MergeExtensionTools(&fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "x", Name: "peek_things", Schema: []byte(`{}`), ReadOnly: true},
		{Extension: "x", Name: "mutate_things", Schema: []byte(`{}`)},
	}})

	if ok, _, _ := gate.Check("peek_things", nil, ""); !ok {
		t.Error("auto-edit should auto-allow the annotated read-only tool")
	}
	// The mutating one falls to the prompt; with a nil inner
	// Confirmer that is a refusal — proving it was NOT auto-allowed.
	if ok, _, _ := gate.Check("mutate_things", nil, ""); ok {
		t.Error("auto-edit must not auto-allow an unannotated extension tool")
	}
}
