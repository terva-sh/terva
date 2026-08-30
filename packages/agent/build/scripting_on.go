//go:build terva_scripting

package build

// The terva_scripting registration seam: this file (and its !terva_scripting
// twin scripting_off.go) is the ONLY place the tag is consulted in this
// package. Everything else goes through the extraBuiltinTools hook, the
// permissions maps this init() extends, and the wireScriptingHostCall
// call in WireHostToolDispatcher — so a surface can never half-support the
// feature by forgetting a tag check (the cli_ctrlproto relaunch-gate bug
// class). Surfaces that need to know ask ScriptingSupported().

import (
	"context"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// ScriptingSupported reports whether this binary carries the jsengine
// scripting consumers (the code_execution and code_execution_mutating
// tools). Built with terva_scripting: true.
func ScriptingSupported() bool { return true }

func init() {
	// code_execution is classified read-only DELIBERATELY: its entire
	// binding set (read/grep/glob) is read-only, so it may run in plan
	// mode and auto-admits in workspace mode like read/grep/glob
	// themselves. The classification follows the binding set — a future
	// mutating binding (bash, write) MUST remove this readOnlyTools entry
	// in the same commit (docs/plans/jsengine-code-execution-and-workflows.md).
	permissions.RegisterReadOnly("code_execution")
	permissions.RegisterBuiltin("code_execution")
	extraBuiltinTools = append(extraBuiltinTools, func(cwd string, sandbox *tools.Sandbox) (string, core.Tool) {
		return "code_execution", &tools.CodeExecutionTool{}
	})

	// code_execution_mutating is the SAME engine with write/edit added, and
	// it is deliberately NOT registered read-only. That single omission is
	// what keeps plan mode's promise: the registry pruning in build.go drops
	// every tool that is neither read-only nor interactive, so the mutating
	// tool never enters a plan-mode registry and the model does not even see
	// it. Adding RegisterReadOnly here would hand plan mode a tool that
	// writes files, silently — which is the regression
	// TestMutatingScriptToolIsNotReadOnly exists to catch.
	//
	// It is a builtin like any other, so permission rules and the audit
	// trail address it by name.
	permissions.RegisterBuiltin("code_execution_mutating")
	extraBuiltinTools = append(extraBuiltinTools, func(cwd string, sandbox *tools.Sandbox) (string, core.Tool) {
		return "code_execution_mutating", &tools.CodeExecutionMutatingTool{}
	})
}

// wireScriptingHostCall late-binds the gated host-tool dispatcher for BOTH
// scripting tools once the live agent and confirm gate exist (the registry
// is built before either). Each script binding call re-enters the SAME
// approval gate a model-issued call uses — reach, not authority, exactly
// like ext host_tool_call — so a script never bypasses approval, and a
// nil gate means yolo mode (allow), matching every other tool path.
//
// Both tools share one dispatcher deliberately: the mutating tool's write
// and edit calls are gated by the very same code path that gates a read,
// so a script's write prompts exactly as a model-issued write does, with
// its own preview and its own audit line.
func wireScriptingHostCall(ag *core.Agent, gate *core.ConfirmGate) {
	dispatch := scriptHostDispatcher(ag, gate)
	catalog := scriptCatalog(ag)
	if tool, ok := ag.LookupTool("code_execution"); ok {
		if ce, ok := tool.(*tools.CodeExecutionTool); ok {
			ce.HostCall = dispatch
			ce.Catalog = catalog
		}
	}
	if tool, ok := ag.LookupTool("code_execution_mutating"); ok {
		if cm, ok := tool.(*tools.CodeExecutionMutatingTool); ok {
			cm.HostCall = dispatch
		}
	}
}

// scriptCatalog computes the session's disclosure catalog (§12.7) from the
// agent: the curated meta/inspection builtins, plus every extension or MCP
// tool the live ReadOnlySet classifies as read-only. Reading a.ReadOnly
// rather than the static permissions map is the §12.5 trap avoided — the
// ReadOnlySet is the same dynamic set the permission policy uses, kept
// current by AdoptReadOnlySet as extensions and MCP servers merge their
// read_only declarations.
//
// Only code_execution takes the catalog; the mutating tool's binding set is
// already the only true mutating pair, so disclosure gains it nothing.
func scriptCatalog(ag *core.Agent) *tools.DisclosureCatalog {
	if ag == nil {
		return nil
	}
	var entries []tools.CatalogEntry
	for name, tool := range ag.Tools {
		if tools.IsCuratedMetaBuiltin(name) {
			entries = append(entries, tools.CatalogEntry{
				Name:        name,
				Description: tool.Description(),
				Schema:      tool.Schema(),
				Source:      "builtin",
			})
			continue
		}
		// The authority-matched half: a plugin tool the session's ReadOnlySet
		// calls read-only. ToolGroup is CoreToolGroup for a builtin, so this
		// admits only extension and MCP tools.
		if core.ToolGroup(tool) != core.CoreToolGroup && ag.ReadOnly.Has(name) {
			entries = append(entries, tools.CatalogEntry{
				Name:        name,
				Description: tool.Description(),
				Schema:      tool.Schema(),
				Source:      core.ToolGroup(tool),
			})
		}
	}
	return tools.NewDisclosureCatalog(entries)
}

// scriptHostDispatcher builds the gated crossing both scripting tools hold.
func scriptHostDispatcher(ag *core.Agent, gate *core.ConfirmGate) func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
	return func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error) {
		target, ok := ag.LookupTool(name)
		if !ok {
			return core.ToolResult{}, fmt.Errorf("no such host tool %q", name)
		}
		// Like ext host_tool_call, this door checks the gate outside the
		// BeforeToolExecute ladder, so it writes its own audit line — up to
		// one per binding call, reads included — and mints its own call id
		// (see hosttool.go: a borrowed id collides in the confirmer's
		// pending map when two parks overlap).
		preview := core.ToolPreview(target, args, 120)
		callID := fmt.Sprintf("script-%s-%d", name, hostGateSeq.Add(1))
		allowed, reason, _ := gate.Check(ctx, name, args, preview, callID)
		recordGateAudit(auditViaScriptBinding, name, args, gate, allowed, reason)
		if !allowed {
			return core.ToolResult{}, fmt.Errorf("%q denied: %s", name, reason)
		}
		return target.Execute(ctx, args, nil)
	}
}
