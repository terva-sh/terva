package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// echoTool is a tool that does nothing, for a gate test that needs an agent
// to pin a tool generation.
type echoTool struct{}

func (echoTool) Name() string            { return "echo" }
func (echoTool) Description() string     { return "echo" }
func (echoTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (echoTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

// agentWith builds an agent over one echo tool, classified by readOnly.
func agentWith(t *testing.T, readOnly *core.ReadOnlySet) *core.Agent {
	t.Helper()
	a, err := core.New(nil, "fake", core.WithTools(core.Registry{"echo": echoTool{}}), core.WithGate(core.AllowAll))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := a.ToolsWithReadOnlySnapshot()
	a.SetToolsWithReadOnly(reg, readOnly)
	return a
}

// The gate classifies a call with the generation the call was pinned to, and
// keeps the approval mode live: a stricter mode set after the pin still
// applies. A child agent's call does not inherit its parent's read-only set.
func TestToolGenerationKeepsModeLiveAndIsolatesAgents(t *testing.T) {
	a := agentWith(t, core.NewReadOnlySet("echo"))
	ctx, _, _ := a.ToolForCall(context.Background(), "echo")
	gate := NewPolicyGate(&PermissionPolicy{Mode: ApprovalWorkspace}, nil)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); !ok {
		t.Fatal("read-only generation was not allowed")
	}
	gate.SetMode(ApprovalAsk)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); ok {
		t.Fatal("pinned metadata hid a stricter live approval mode")
	}
	child := agentWith(t, nil)
	ctx, _, _ = child.ToolForCall(ctx, "echo")
	gate.SetMode(ApprovalWorkspace)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); ok {
		t.Fatal("child inherited its parent's read-only authority")
	}
}

// A rule outranks the pinned classification, in every mode and for both
// classes: only an allow rule allows, and plan mode still refuses a tool
// that mutates.
func TestToolGenerationPreservesRulePrecedence(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalWorkspace, ApprovalAutoEdit, ApprovalPlan} {
		for _, readOnly := range []bool{true, false} {
			for _, decision := range []RuleDecision{RuleAllow, RuleDeny, RuleAsk} {
				ro := core.NewReadOnlySet()
				if readOnly {
					ro.Add("echo")
				}
				a := agentWith(t, ro)
				ctx, _, _ := a.ToolForCall(context.Background(), "echo")
				gate := NewPolicyGate(&PermissionPolicy{Mode: mode}, nil)
				gate.SetRules([]PermissionRule{{Tool: "echo", Decision: decision, Source: "user"}})
				allowed, _, _ := gate.Check(ctx, "echo", nil, "", "")
				want := decision == RuleAllow && (mode != ApprovalPlan || readOnly)
				if allowed != want {
					t.Fatalf("mode=%s readOnly=%v rule=%s allowed=%v", mode, readOnly, decision, allowed)
				}
			}
		}
	}
}

// ranTool records whether it executed.
type ranTool struct {
	echoTool
	ran bool
}

func (r *ranTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	r.ran = true
	return core.ToolResult{}, nil
}

// reloadClient answers the first request of a turn with one call to echo,
// running onFirst before it replies, and every later request with text.
type reloadClient struct {
	calls   int
	onFirst func()
}

func (c *reloadClient) Name() string { return "reload" }

func (c *reloadClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.calls++
	first := c.calls == 1
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "reload", Model: req.Model}
		if first {
			if c.onFirst != nil {
				c.onFirst()
			}
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "T1", Name: "echo", Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// A ConfirmGate classifies an in-flight call with the generation its turn
// pinned, through a real turn. A reload mid-turn reaches the next turn and
// not the call in flight, in every mode that consults the classification.
// No Confirmer is set, so a call the policy would prompt for is refused.
func TestTurnPinsToolClassificationDuringReload(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalWorkspace, ApprovalAutoEdit, ApprovalPlan} {
		for _, wasReadOnly := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/read-only=%t", mode, wasReadOnly), func(t *testing.T) {
				oldTool, newTool := &ranTool{}, &ranTool{}
				oldSet, newSet := core.NewReadOnlySet(), core.NewReadOnlySet()
				if wasReadOnly {
					oldSet.Add("echo")
				} else {
					newSet.Add("echo")
				}
				gate := NewPolicyGate(&PermissionPolicy{Mode: mode, ReadOnly: oldSet}, nil)
				client := &reloadClient{}
				a, err := core.New(client, "fake",
					core.WithTools(core.Registry{"echo": oldTool}),
					core.WithGate(core.GateFunc(func(ctx context.Context, call provider.ToolCallBlock, _ core.Tool) (bool, string, json.RawMessage) {
						return gate.Check(ctx, call.Name, call.Arguments, "", call.ID)
					})))
				if err != nil {
					t.Fatal(err)
				}
				a.SetToolsWithReadOnly(core.Registry{"echo": oldTool}, oldSet)
				blocked, resume := make(chan struct{}), make(chan struct{})
				client.onFirst = func() { close(blocked); <-resume }
				done := make(chan error, 1)
				go func() { done <- a.Prompt(context.Background(), "first", nil, nil) }()
				<-blocked
				a.SetToolsWithReadOnly(core.Registry{"echo": newTool}, newSet)
				close(resume)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if oldTool.ran != wasReadOnly || newTool.ran {
					t.Fatalf("in-flight call crossed generations: old=%v new=%v", oldTool.ran, newTool.ran)
				}
				client.calls, client.onFirst = 0, nil
				if err := a.Prompt(context.Background(), "next", nil, nil); err != nil {
					t.Fatal(err)
				}
				if newTool.ran == wasReadOnly {
					t.Fatalf("next turn did not use the new classification: executed=%v", newTool.ran)
				}
			})
		}
	}
}
