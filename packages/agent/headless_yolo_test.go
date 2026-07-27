package agent

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extensions"
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
	gate, _ := build.HeadlessConfirmGate(build.Args{NoYolo: true, CWD: testsupport.TempDir(t)}, "print")
	if gate == nil {
		t.Fatal("headlessConfirmGate returned nil with NoYolo set; want a refusing gate")
	}
	ok, reason, _ := gate.Check("bash", nil, "ls", "")
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
	if g, _ := build.HeadlessConfirmGate(build.Args{NoYolo: false, CWD: testsupport.TempDir(t)}, "json"); g != nil {
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
	gate, _ := build.HeadlessConfirmGate(build.Args{NoYolo: true, CWD: testsupport.TempDir(t)}, "print")

	wireNonInteractiveAgentExtHooks(context.Background(), ag, extMgr, gate, nil, nil, nil)

	if ag.BeforeToolExecute == nil {
		t.Fatal("BeforeToolExecute was not installed")
	}
	allowed, reason, _ := ag.BeforeToolExecute(provider.ToolCallBlock{
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

	allowed, reason, _ := ag.BeforeToolExecute(provider.ToolCallBlock{
		ID:        "T1",
		Name:      "bash",
		Arguments: []byte(`{"command":"ls"}`),
	})
	if !allowed {
		t.Fatalf("BeforeToolExecute refused with yolo on (reason=%q); want allow", reason)
	}
}
