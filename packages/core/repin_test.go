package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// twoStepClient asks for the move tool, then the echo tool, then ends.
type twoStepClient struct {
	mu    sync.Mutex
	calls int
}

func (c *twoStepClient) Name() string { return "two-step" }

func (c *twoStepClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	msg, stop := provider.Message{Role: provider.RoleAssistant}, provider.StopToolUse
	switch call {
	case 1:
		msg.Content = []provider.Content{provider.ToolCallBlock{ID: "M1", Name: "move", Arguments: json.RawMessage(`{}`)}}
	case 2:
		msg.Content = []provider.Content{provider.ToolCallBlock{ID: "E1", Name: "echo", Arguments: json.RawMessage(`{}`)}}
	default:
		msg.Content = []provider.Content{provider.TextBlock{Text: "done"}}
		stop = provider.StopEnd
	}
	out := make(chan provider.Event, 2)
	out <- provider.EventStart{Provider: "two-step", Model: req.Model}
	out <- provider.EventDone{Stop: stop, Message: msg}
	close(out)
	return out, nil
}

// funcTool runs fn as its Execute.
type funcTool struct {
	name string
	fn   func()
}

func (f *funcTool) Name() string            { return f.name }
func (f *funcTool) Description() string     { return f.name }
func (f *funcTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *funcTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	if f.fn != nil {
		f.fn()
	}
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// A tool that publishes new tools and calls RequestRepin moves the rest of
// its turn onto them: the next call in the same turn runs the new instance.
// Without the request, the turn keeps its pin and the old instance runs, as
// TestTurnPinsToolClassificationDuringReload requires.
func TestRequestRepinMovesTheRestOfTheTurnOntoNewTools(t *testing.T) {
	for _, request := range []bool{true, false} {
		oldEcho, newEcho := &recordingTool{}, &recordingTool{}
		var a *Agent
		move := &funcTool{name: "move"}
		move.fn = func() {
			a.SetToolsWithReadOnly(Registry{"move": move, "echo": newEcho}, nil)
			if request {
				a.RequestRepin()
			}
		}
		a = newTestAgent(&twoStepClient{}, "fake", "", Registry{"move": move, "echo": oldEcho})
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatal(err)
		}
		ranNew, ranOld := newEcho.lastArgs != nil, oldEcho.lastArgs != nil
		if request && (!ranNew || ranOld) {
			t.Errorf("with RequestRepin, the next call must run the new tool: new=%v old=%v", ranNew, ranOld)
		}
		if !request && (ranNew || !ranOld) {
			t.Errorf("without RequestRepin, the turn keeps its pin: new=%v old=%v", ranNew, ranOld)
		}
		if a.repinRequested.Load() {
			t.Error("the loop must clear the request when it re-pins")
		}
	}
}

// A request made between turns is satisfied by the next turn's first pin, so
// it must not also re-pin that turn after its first tool batch. Here the
// stale request comes before the turn, and a tool then publishes new tools
// without asking: the turn keeps its pin and runs the old instance.
func TestAStaleRepinRequestDoesNotCarryIntoTheNextTurn(t *testing.T) {
	oldEcho, newEcho := &recordingTool{}, &recordingTool{}
	var a *Agent
	move := &funcTool{name: "move"}
	move.fn = func() { a.SetToolsWithReadOnly(Registry{"move": move, "echo": newEcho}, nil) }
	a = newTestAgent(&twoStepClient{}, "fake", "", Registry{"move": move, "echo": oldEcho})
	a.RequestRepin()
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if newEcho.lastArgs != nil || oldEcho.lastArgs == nil {
		t.Fatalf("a stale request re-pinned the turn: new=%v old=%v", newEcho.lastArgs != nil, oldEcho.lastArgs != nil)
	}
}
