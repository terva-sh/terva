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
// scripting consumer (the code_execution tool). Built with terva_scripting:
// true.
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
}

// wireScriptingHostCall late-binds code_execution's gated host-tool
// dispatcher once the live agent and confirm gate exist (the registry is
// built before either). Each script binding call re-enters the SAME
// approval gate a model-issued call uses — reach, not authority, exactly
// like ext host_tool_call — so a script never bypasses approval, and a
// nil gate means yolo mode (allow), matching every other tool path.
func wireScriptingHostCall(ag *core.Agent, gate *core.ConfirmGate) {
	tool, ok := ag.LookupTool("code_execution")
	if !ok {
		return
	}
	ce, ok := tool.(*tools.CodeExecutionTool)
	if !ok {
		return
	}
	ce.HostCall = func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error) {
		target, ok := ag.LookupTool(name)
		if !ok {
			return core.ToolResult{}, fmt.Errorf("no such host tool %q", name)
		}
		// Like ext host_tool_call, this door checks the gate outside the
		// BeforeToolExecute ladder, so it writes its own audit line — up to
		// one per binding call, reads included — and mints its own call id
		// (see hosttool.go: a borrowed id collides in the confirmer's
		// pending map when two parks overlap).
		preview := core.BuildPreview(args, 120)
		callID := fmt.Sprintf("script-%s-%d", name, hostGateSeq.Add(1))
		allowed, reason, _ := gate.Check(name, args, preview, callID)
		recordGateAudit(auditViaScriptBinding, name, args, gate, allowed, reason)
		if !allowed {
			return core.ToolResult{}, fmt.Errorf("%q denied: %s", name, reason)
		}
		return target.Execute(ctx, args, nil)
	}
}
