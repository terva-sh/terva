package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestHeadlessConfirmGateRefusesWhenNoYolo verifies that headless
// modes (print / json / rpc / swarm-agent) build a refusing gate when
// --no-yolo is set: a gate with a nil inner Confirmer that denies
// every not-yet-allowed tool call. This is the deliberate upstream
// behavior break — headless --no-yolo refuses tools instead of running
// them unconfirmed.
func TestHeadlessConfirmGateRefusesWhenNoYolo(t *testing.T) {
	withTempHome(t) // isolate from any real config / installed-extension rules
	gate, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.Print, NoYolo: true, CWD: testsupport.TempDir(t)}.PermInputs())
	if gate == nil {
		t.Fatal("headlessConfirmGate returned nil with NoYolo set; want a refusing gate")
	}
	ok, reason, _ := gate.Check(context.Background(), "bash", nil, "ls", "")
	if ok {
		t.Fatal("gate allowed a tool call under --no-yolo; want refusal")
	}
	if reason == "" {
		t.Fatal("gate refused without a model-readable reason")
	}
}

// TestHeadlessConfirmGateNilWhenYolo verifies that without --no-yolo
// there is no gate (yolo mode runs tools unconfirmed, as before).
func TestHeadlessConfirmGateNilWhenYolo(t *testing.T) {
	withTempHome(t) // a real installed extension's permission rules would otherwise force a gate
	if g, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.JSON, NoYolo: false, CWD: testsupport.TempDir(t)}.PermInputs()); g != nil {
		t.Fatalf("headlessConfirmGate returned non-nil with yolo on: %v", g)
	}
}

// TestWireNonInteractiveGateRefusesToolCall verifies the wiring: once
// wireNonInteractiveAgentExtHooks installs a refusing gate, the
// agent's BeforeToolExecute closure (the exact callback the agent
// invokes before every tool — see core/agent.go runOneTool) denies the
// call with a model-readable reason, before the extension intercept
// ever sees it.
func TestWireNonInteractiveGateRefusesToolCall(t *testing.T) {
	withTempHome(t)
	ag := core.NewAgent(nil, "test", "", core.Registry{})
	extMgr := extensions.New(testsupport.TempDir(t), testsupport.TempDir(t), "test", "openai", "gpt-5", build.NonInteractiveExtHooks{})
	gate, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.Print, NoYolo: true, CWD: testsupport.TempDir(t)}.PermInputs())

	wireNonInteractiveAgentExtHooks(context.Background(), ag, extMgr, gate, nil, nil, nil)

	if ag.BeforeToolExecute == nil {
		t.Fatal("BeforeToolExecute was not installed")
	}
	allowed, reason, _ := ag.BeforeToolExecute(t.Context(), provider.ToolCallBlock{
		ID:        "T1",
		Name:      "bash",
		Arguments: []byte(`{"command":"rm -rf /"}`),
	})
	if allowed {
		t.Fatal("BeforeToolExecute allowed a tool call under --no-yolo; want refusal")
	}
	if !strings.Contains(reason, "no-yolo") && !strings.Contains(reason, "refused") {
		t.Errorf("refusal reason is not model-readable: %q", reason)
	}
}

// TestWireNonInteractiveNoGateAllowsToolCall verifies that with no
// gate (yolo), BeforeToolExecute does not refuse on the gate's behalf:
// a bare extension manager (no subscribers) lets the call through.
func TestWireNonInteractiveNoGateAllowsToolCall(t *testing.T) {
	ag := core.NewAgent(nil, "test", "", core.Registry{})
	extMgr := extensions.New(testsupport.TempDir(t), testsupport.TempDir(t), "test", "openai", "gpt-5", build.NonInteractiveExtHooks{})

	wireNonInteractiveAgentExtHooks(context.Background(), ag, extMgr, nil, nil, nil, nil)

	allowed, reason, _ := ag.BeforeToolExecute(t.Context(), provider.ToolCallBlock{
		ID:        "T1",
		Name:      "bash",
		Arguments: []byte(`{"command":"ls"}`),
	})
	if !allowed {
		t.Fatalf("BeforeToolExecute refused with yolo on (reason=%q); want allow", reason)
	}
}

// TestTheGateIsWiredWithNoExtensionManager: the permission gate is not
// conditional on extensions.
//
// The helper used to open with `if ag == nil || extMgr == nil { return }`, above
// the line that installs BeforeToolExecute — so a host with a gate and no
// extension manager got an agent with NO ladder at all, and every tool call ran
// unasked. Nothing reached it (every caller's manager comes from
// setupNonInteractiveExtensions, which always builds one), and the SDK avoids
// the helper precisely because it wires a gate with no manager. That is a lot of
// load for an early return to be carrying.
func TestTheGateIsWiredWithNoExtensionManager(t *testing.T) {
	withTempHome(t)
	ag := core.NewAgent(nil, "test", "", core.Registry{})
	gate, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.Print, NoYolo: true, CWD: testsupport.TempDir(t)}.PermInputs())

	wireNonInteractiveAgentExtHooks(context.Background(), ag, nil, gate, nil, nil, nil)

	if ag.BeforeToolExecute == nil {
		t.Fatal("no ladder was installed for an agent with no extension manager — every tool call would " +
			"run unasked, gate or no gate")
	}
	allowed, reason, _ := ag.BeforeToolExecute(t.Context(), provider.ToolCallBlock{
		ID: "T1", Name: "bash", Arguments: []byte(`{"command":"rm -rf /"}`),
	})
	if allowed {
		t.Fatalf("a gated agent with no extensions allowed a tool call (reason=%q)", reason)
	}
}

// TestTheTaskBoardDoesNotDependOnExtensions: the built-in board's context card
// follows the CONTROLLER, not the manager. It was wired inside the extension
// check, which made a first-party feature's visibility depend on an unrelated
// subsystem being present.
func TestTheTaskBoardDoesNotDependOnExtensions(t *testing.T) {
	withTempHome(t)
	dir := testsupport.TempDir(t)
	ctrl := tasktool.New(tasks.NewStore(tasks.NewLayeredFS(filepath.Join(dir, "tasks"), filepath.Join(dir, "ext")), "agent"))
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "still visible"}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	ag := core.NewAgent(nil, "test", "", core.Registry{})
	wireNonInteractiveAgentExtHooks(context.Background(), ag, nil, nil, nil, nil, ctrl)

	if ag.ContextProvider == nil {
		t.Fatal("no per-turn context provider: the task card is missing because there were no extensions")
	}
	if got := ag.ContextProvider(); !strings.Contains(got, "still visible") {
		t.Errorf("the task card did not reach the model's context: %q", got)
	}
}
