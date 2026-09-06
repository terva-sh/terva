package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestTurnPinsToolClassificationDuringReload(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalWorkspace, ApprovalAutoEdit, ApprovalPlan} {
		for _, wasReadOnly := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/read-only=%t", mode, wasReadOnly), func(t *testing.T) {
				oldTool, newTool := &recordingTool{}, &recordingTool{}
				oldSet, newSet := NewReadOnlySet(), NewReadOnlySet()
				if wasReadOnly {
					oldSet.Add("echo")
				} else {
					newSet.Add("echo")
				}
				client := &pinProbeClient{}
				a := NewAgent(client, "fake", "", Registry{"echo": oldTool})
				a.ReadOnly = oldSet
				gate := NewPolicyGate(&PermissionPolicy{Mode: mode, ReadOnly: oldSet}, nil)
				a.BeforeToolExecute = func(ctx context.Context, call provider.ToolCallBlock) (bool, string, json.RawMessage) {
					return gate.Check(ctx, call.Name, call.Arguments, "", call.ID)
				}
				blocked, resume := make(chan struct{}), make(chan struct{})
				client.onFirst = func() { close(blocked); <-resume }
				done := make(chan error, 1)
				go func() { done <- a.Prompt(context.Background(), "first", nil, nil) }()
				<-blocked
				a.SetToolsWithReadOnly(Registry{"echo": newTool}, newSet)
				// Later edits to the assembly set cannot change the published set.
				newSet.Add("echo")
				close(resume)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if (oldTool.lastArgs != nil) != wasReadOnly || newTool.lastArgs != nil {
					t.Fatalf("in-flight dispatch crossed generations: old=%v new=%v", oldTool.lastArgs, newTool.lastArgs)
				}
				client.calls, client.onFirst = 0, nil
				if err := a.Prompt(context.Background(), "next", nil, nil); err != nil {
					t.Fatal(err)
				}
				if (newTool.lastArgs != nil) == wasReadOnly {
					t.Fatalf("next turn did not use the new classification: executed=%v", newTool.lastArgs != nil)
				}
			})
		}
	}
}

func TestToolGenerationKeepsModeLiveAndIsolatesAgents(t *testing.T) {
	a := NewAgent(nil, "fake", "", Registry{"echo": &recordingTool{}})
	a.ReadOnly = NewReadOnlySet("echo")
	ctx, _, _ := a.ToolForCall(context.Background(), "echo")
	gate := NewPolicyGate(&PermissionPolicy{Mode: ApprovalWorkspace}, nil)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); !ok {
		t.Fatal("read-only generation was not allowed")
	}
	gate.SetMode(ApprovalAsk)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); ok {
		t.Fatal("pinned metadata hid a stricter live approval mode")
	}
	child := NewAgent(nil, "fake", "", Registry{"echo": &recordingTool{}})
	child.SetToolsWithReadOnly(child.Tools, nil)
	ctx, _, _ = child.ToolForCall(ctx, "echo")
	gate.SetMode(ApprovalWorkspace)
	if ok, _, _ := gate.Check(ctx, "echo", nil, "", ""); ok {
		t.Fatal("child inherited its parent's read-only authority")
	}
}

func TestToolGenerationPreservesRulePrecedence(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalWorkspace, ApprovalAutoEdit, ApprovalPlan} {
		for _, readOnly := range []bool{true, false} {
			for _, decision := range []RuleDecision{RuleAllow, RuleDeny, RuleAsk} {
				a := NewAgent(nil, "fake", "", Registry{"echo": &recordingTool{}})
				ro := NewReadOnlySet()
				if readOnly {
					ro.Add("echo")
				}
				a.SetToolsWithReadOnly(a.Tools, ro)
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
