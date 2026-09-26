package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"terva.sh/terva/packages/provider"
)

// A turn dispatches, and classifies, every call with the tool generation it
// pinned when it began. A reload mid-turn reaches the next turn and not the
// calls still in flight. The gate here reads the classification the way a
// host's gate does, through ReadOnlyForCall, and allows read-only tools only.
func TestTurnPinsToolClassificationDuringReload(t *testing.T) {
	for _, wasReadOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("read-only=%t", wasReadOnly), func(t *testing.T) {
			oldTool, newTool := &recordingTool{}, &recordingTool{}
			oldSet, newSet := NewReadOnlySet(), NewReadOnlySet()
			if wasReadOnly {
				oldSet.Add("echo")
			} else {
				newSet.Add("echo")
			}
			client := &pinProbeClient{}
			a := newTestAgent(client, "fake", "", Registry{"echo": oldTool})
			a.SetToolsWithReadOnly(a.tools, oldSet)
			a.gate = GateFunc(func(ctx context.Context, call provider.ToolCallBlock, _ Tool) (bool, string, json.RawMessage) {
				return ReadOnlyForCall(ctx).Has(call.Name), "not read-only", nil
			})
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

// ReadOnlyForCall reports the generation ToolForCall pinned, and nothing
// outside one. What it returns is a copy: a gate that adds to it cannot widen
// the turn's authority. A child agent pins its own generation, even under a
// context that carries its parent's.
func TestReadOnlyForCallReadsThePinnedGeneration(t *testing.T) {
	if got := ReadOnlyForCall(context.Background()); got != nil {
		t.Fatalf("a context with no generation returned %v, want nil", got)
	}
	a := newTestAgent(nil, "fake", "", Registry{"echo": &recordingTool{}, "rm": &recordingTool{}})
	a.SetToolsWithReadOnly(a.tools, NewReadOnlySet("echo"))
	ctx, _, _ := a.ToolForCall(context.Background(), "echo")
	got := ReadOnlyForCall(ctx)
	if !got.Has("echo") || got.Has("rm") {
		t.Fatalf("pinned set: echo=%v rm=%v, want echo only", got.Has("echo"), got.Has("rm"))
	}
	got.Add("rm")
	if ReadOnlyForCall(ctx).Has("rm") {
		t.Fatal("an edit to the returned set reached the pinned generation")
	}
	child := newTestAgent(nil, "fake", "", Registry{"echo": &recordingTool{}})
	child.SetToolsWithReadOnly(child.tools, nil)
	ctx, _, _ = child.ToolForCall(ctx, "echo")
	if ReadOnlyForCall(ctx).Has("echo") {
		t.Fatal("the child inherited its parent's read-only set")
	}
}
