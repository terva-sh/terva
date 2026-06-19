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

// TestAuthorityOverridesReadOnly pins the Phase A classification: a
// declared authority decides read-only-ness, so a network-read tool is
// gated like a side-effecting tool (not auto-allowed, refused in plan)
// even if it also set the legacy read_only bool, while a local-read
// authority is treated as read-only.
func TestAuthorityOverridesReadOnly(t *testing.T) {
	pol := &core.PermissionPolicy{
		Mode:      core.ApprovalWorkspace,
		ReadOnly:  builtinReadOnlySet(),
		EditTools: editTools,
		Builtin:   builtinTools,
	}
	gate := core.NewPolicyGate(pol, nil)
	r := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalWorkspace}
	r.AdoptReadOnlySet(pol.ReadOnly)
	r.MergeExtensionTools(&fakeToolSource{infos: []ExtensionToolInfo{
		// Declares network-read AND the legacy read_only bool: authority
		// wins, so it must NOT be auto-allowed as read-only.
		{Extension: "web", Name: "web_fetch", Schema: []byte(`{}`), ReadOnly: true, Authority: string(core.AuthNetworkRead)},
		// Declares local-read: auto-allowable like a read-only tool.
		{Extension: "x", Name: "peek_things", Schema: []byte(`{}`), Authority: string(core.AuthLocalRead)},
	}})

	if pol.ReadOnly.Has("web_fetch") {
		t.Error("network-read tool must not join the read-only set despite read_only=true")
	}
	if ok, _, _ := gate.Check("web_fetch", nil, ""); ok {
		t.Error("workspace must not auto-allow a network-read tool (it should prompt/refuse)")
	}
	if !pol.ReadOnly.Has("peek_things") {
		t.Error("local-read authority should join the read-only set")
	}
	if ok, _, _ := gate.Check("peek_things", nil, ""); !ok {
		t.Error("workspace should auto-allow a local-read tool")
	}
}

// TestAuthorityNetworkReadExcludedFromPlan ensures a network-read tool
// is not admitted to the plan-mode registry (plan permits local reads
// only), even with read_only=true set.
func TestAuthorityNetworkReadExcludedFromPlan(t *testing.T) {
	pol := &core.PermissionPolicy{Mode: core.ApprovalPlan, ReadOnly: builtinReadOnlySet(), EditTools: editTools}
	r := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalPlan}
	r.AdoptReadOnlySet(pol.ReadOnly)
	r.MergeExtensionTools(&fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "web", Name: "web_fetch", Schema: []byte(`{}`), ReadOnly: true, Authority: string(core.AuthNetworkRead)},
		{Extension: "x", Name: "peek_things", Schema: []byte(`{}`), Authority: string(core.AuthLocalRead)},
	}})
	if _, ok := r.ToolRegistry["web_fetch"]; ok {
		t.Error("network-read tool must stay out of the plan-mode registry")
	}
	if _, ok := r.ToolRegistry["peek_things"]; !ok {
		t.Error("local-read tool should join the plan-mode registry")
	}
}
