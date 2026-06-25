package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// pinProbeClient drives a two-step turn (tool_use → end) and records every
// request. On the first call it runs onFirst, simulating a host that swaps
// the cached prefix (system + tools) mid-turn — the disruptive case the
// runLoop pin must absorb.
type pinProbeClient struct {
	calls   int
	reqs    []provider.Request
	onFirst func()
}

func (c *pinProbeClient) Name() string { return "pin-probe" }

func (c *pinProbeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.calls++
	c.reqs = append(c.reqs, req)
	call := c.calls
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "pin-probe", Model: req.Model}
		if call == 1 {
			if c.onFirst != nil {
				c.onFirst() // swap the agent's prefix between step 1 and step 2
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

// TestTurnPinsPrefixAgainstMidTurnSwap is the core guarantee behind the
// feature: a host swapping the system prompt and the tool set mid-turn (an
// extension's refresh_context / set_withdrawn_tools, or /reload-ext) must
// not change the in-flight turn. The model is still offered — and dispatched
// against — the prefix the turn started with; the swap takes effect only on
// the NEXT prompt.
func TestTurnPinsPrefixAgainstMidTurnSwap(t *testing.T) {
	rec := &recordingTool{}
	client := &pinProbeClient{}
	a := NewAgent(client, "fake-model", "orig-system", Registry{"echo": rec})

	// Between step 1 and step 2, withdraw the tool and rewrite the system
	// block — exactly what a mid-turn host swap would do.
	client.onFirst = func() {
		a.SetTools(Registry{}) // echo gone from the LIVE registry
		a.mu.Lock()
		a.System = "CHANGED-MID-TURN"
		a.mu.Unlock()
	}

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if len(client.reqs) != 2 {
		t.Fatalf("expected 2 model calls (tool_use then end); got %d", len(client.reqs))
	}
	// Step 2's request must still carry the PINNED prefix, not the swap.
	if client.reqs[1].System != "orig-system" {
		t.Errorf("step 2 System = %q; want the pinned %q (mid-turn swap leaked into the turn)", client.reqs[1].System, "orig-system")
	}
	if len(client.reqs[1].Tools) != 1 {
		t.Errorf("step 2 offered %d tools; want 1 (echo pinned for the turn, not the withdrawn live set)", len(client.reqs[1].Tools))
	}
	// And the tool actually dispatched, despite being withdrawn live —
	// proving dispatch used the pinned registry, not a.Tools.
	if rec.lastArgs == nil {
		t.Error("echo never executed; dispatch used the live (emptied) registry instead of the pinned one")
	}

	// The swap is not lost — a fresh turn picks it up.
	if err := a.Prompt(context.Background(), "again", nil, nil); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	last := client.reqs[len(client.reqs)-1]
	if last.System != "CHANGED-MID-TURN" {
		t.Errorf("next turn System = %q; want the swapped %q to take effect at the boundary", last.System, "CHANGED-MID-TURN")
	}
	if len(last.Tools) != 0 {
		t.Errorf("next turn offered %d tools; want 0 (the withdrawal applies at the boundary)", len(last.Tools))
	}
}

// TestRunOneToolUsesPinnedRegistryRaceFree pins dispatch to the passed
// registry, decoupled from a.Tools. It must (a) dispatch against the pinned
// set even when the live registry was emptied, and (b) never race a
// concurrent SetTools. Run with -race for (b) to mean anything.
func TestRunOneToolUsesPinnedRegistryRaceFree(t *testing.T) {
	rec := &recordingTool{}
	a := NewAgent(nil, "test", "", Registry{})
	a.SetTools(Registry{}) // live registry is EMPTY...
	pinned := Registry{"echo": rec}

	ctx := context.Background()
	call := provider.ToolCallBlock{ID: "T1", Name: "echo", Arguments: json.RawMessage(`{}`)}

	// (a) dispatch succeeds against the pinned set despite an empty a.Tools.
	if res := a.runOneTool(ctx, call, pinned, func(AgentEvent) {}); res.IsError {
		t.Fatalf("dispatch against pinned registry failed: %v", res.Content)
	}

	// (b) concurrent SetTools churn must not race the pinned dispatch.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			a.runOneTool(ctx, call, pinned, func(AgentEvent) {})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 200 {
			if i%2 == 0 {
				a.SetTools(Registry{"echo": rec})
			} else {
				a.SetTools(Registry{})
			}
		}
	}()
	wg.Wait()
}
