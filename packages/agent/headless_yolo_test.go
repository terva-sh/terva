package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/internal/coretest"
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

// resolvedForGateTest resolves a real, credentialed run for the gate tests
// below, so each builds its agent through the same Resolved.NewAgent every
// headless host uses. No request is ever sent.
func resolvedForGateTest(t *testing.T) build.Resolved {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := build.Resolve(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestNonInteractiveAgentGateRefusesToolCall verifies the wiring: an agent
// built the way the headless hosts build it carries the refusing gate as its
// constructor argument, and that gate (the exact one the agent asks before every tool; see
// core/agent.go runOneTool) denies the call with a model-readable reason,
// before the extension intercept ever sees it.
func TestNonInteractiveAgentGateRefusesToolCall(t *testing.T) {
	withTempHome(t)
	r := resolvedForGateTest(t)
	extMgr := extensions.New(testsupport.TempDir(t), testsupport.TempDir(t), "test", "openai", "gpt-5", build.NonInteractiveExtHooks{})
	gate, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.Print, NoYolo: true, CWD: testsupport.TempDir(t)}.PermInputs())

	ag := r.NewAgent(build.BuildToolGate(nil, gate, extMgr))

	allowed, reason, _ := ag.Gate().CheckTool(t.Context(), provider.ToolCallBlock{
		ID:        "T1",
		Name:      "bash",
		Arguments: []byte(`{"command":"rm -rf /"}`),
	}, nil)
	if allowed {
		t.Fatal("the agent's gate allowed a tool call under --no-yolo; want refusal")
	}
	if !strings.Contains(reason, "no-yolo") && !strings.Contains(reason, "refused") {
		t.Errorf("refusal reason is not model-readable: %q", reason)
	}
}

// TestNonInteractiveAgentNoGateAllowsToolCall verifies that with no confirm
// gate (yolo), the ladder does not refuse on the gate's behalf: a bare
// extension manager (no subscribers) lets the call through.
func TestNonInteractiveAgentNoGateAllowsToolCall(t *testing.T) {
	withTempHome(t)
	r := resolvedForGateTest(t)
	extMgr := extensions.New(testsupport.TempDir(t), testsupport.TempDir(t), "test", "openai", "gpt-5", build.NonInteractiveExtHooks{})

	ag := r.NewAgent(build.BuildToolGate(nil, nil, extMgr))

	allowed, reason, _ := ag.Gate().CheckTool(t.Context(), provider.ToolCallBlock{
		ID:        "T1",
		Name:      "bash",
		Arguments: []byte(`{"command":"ls"}`),
	}, nil)
	if !allowed {
		t.Fatalf("the ladder refused with yolo on (reason=%q); want allow", reason)
	}
}

// TestTheGateIsWiredWithNoExtensionManager: the permission gate is not
// conditional on extensions.
//
// wireNonInteractiveAgentExtHooks used to install the ladder, below an early
// return for `ag == nil || extMgr == nil`, so a host with a gate and no
// extension manager got an agent with NO ladder at all, and every tool call ran
// unasked. The ladder is now the agent's constructor argument; this pins that a
// nil manager still yields a gate that refuses.
func TestTheGateIsWiredWithNoExtensionManager(t *testing.T) {
	withTempHome(t)
	r := resolvedForGateTest(t)
	gate, _ := permissions.HeadlessConfirmGate(build.Args{Mode: mode.Print, NoYolo: true, CWD: testsupport.TempDir(t)}.PermInputs())

	ag := r.NewAgent(build.BuildToolGate(nil, gate, nil))
	wireNonInteractiveAgentExtHooks(context.Background(), ag, nil, gate, nil, nil, nil)

	allowed, reason, _ := ag.Gate().CheckTool(t.Context(), provider.ToolCallBlock{
		ID: "T1", Name: "bash", Arguments: []byte(`{"command":"rm -rf /"}`),
	}, nil)
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

	ag := coretest.NewAgentWithAssembler(nil, "test", build.NewAssembler(nil), core.Registry{})
	wireNonInteractiveAgentExtHooks(context.Background(), ag, nil, nil, nil, nil, ctrl)

	if got := ag.FramePreview().VolatileText(); !strings.Contains(got, "still visible") {
		t.Errorf("the task card did not reach the model's context: %q", got)
	}
}
