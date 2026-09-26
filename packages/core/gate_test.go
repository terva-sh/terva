package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// ptrGate is a pointer type implementing Gate, so a nil *ptrGate is a
// non-nil Gate holding a nil value.
type ptrGate struct{ allow bool }

func (g *ptrGate) CheckTool(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
	return g.allow, "", nil
}

// A nil gate is refused at construction, in every spelling. A nil GateFunc
// and a nil pointer to a Gate type are the sneaky ones: each is a non-nil
// interface, so a check for `gate == nil` alone passes it, and the first tool
// call then panics inside the turn instead of at the line that made the
// mistake. The typed-nil pointer was found by the advisory review of #1333.
func TestNewRefusesANilGate(t *testing.T) {
	for name, g := range map[string]Gate{
		"nil interface": nil,
		"nil GateFunc":  GateFunc(nil),
		"nil *ptrGate":  (*ptrGate)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			a, err := New(nil, "m", WithGate(g))
			if a != nil || !errors.Is(err, ErrNilGate) {
				t.Fatalf("New with a %s gate returned %v, %v; want ErrNilGate", name, a, err)
			}
			if !strings.Contains(err.Error(), "AllowAll") {
				t.Errorf("the error does not name the explicit way out: %v", err)
			}
		})
	}
}

// An Agent built without New has no gate, and fails closed. Before the
// gate was a constructor argument, this shape ran every call unchecked.
func TestAnAgentWithNoGateRefusesEveryToolCall(t *testing.T) {
	tool := &cancelProbeTool{}
	ag := &Agent{}
	res := ag.runOneTool(context.Background(), recorderCall(), Registry{"recorder": tool}, func(AgentEvent) {})
	if tool.ran.Load() {
		t.Fatal("a tool ran on an agent with no gate")
	}
	if !res.IsError || !strings.Contains(resultText(res), "core.New") {
		t.Errorf("want an error result that names core.New, got %+v", res)
	}
}

// The gate receives the implementation the call will run, taken from the
// registry pinned for the turn. This is what lets a gate show a tool's Preview
// without a handle on the agent, which is what let hosts build the gate first.
func TestTheGateReceivesTheResolvedTool(t *testing.T) {
	tool := &cancelProbeTool{}
	var seen Tool
	var seenCall provider.ToolCallBlock
	ag := newTestAgentWithGate(nil, "m", "", Registry{"recorder": tool}, GateFunc(func(_ context.Context, call provider.ToolCallBlock, t Tool) (bool, string, json.RawMessage) {
		seen, seenCall = t, call
		return true, "", nil
	}))
	ag.runOneTool(context.Background(), recorderCall(), ag.tools, func(AgentEvent) {})
	if seen != Tool(tool) {
		t.Errorf("the gate saw tool %v, want the registry's %v", seen, tool)
	}
	if seenCall.ID != "call-1" || seenCall.Name != "recorder" {
		t.Errorf("the gate saw call %+v", seenCall)
	}
	if !tool.ran.Load() {
		t.Error("the gate allowed the call but the tool did not run")
	}
	if ag.Gate() == nil {
		t.Error("Gate() returned nil for an agent built with a gate")
	}
}

// AllowAll allows, and rewrites nothing.
func TestAllowAllAllowsAndRewritesNothing(t *testing.T) {
	ok, reason, args := AllowAll.CheckTool(context.Background(), recorderCall(), nil)
	if !ok || reason != "" || args != nil {
		t.Errorf("AllowAll = (%v, %q, %s), want (true, \"\", nil)", ok, reason, args)
	}
}
