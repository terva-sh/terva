package core

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/provider"
)

// innerReportingTool calls a host tool on its own behalf and reports it, as
// code_execution does, then answers "ok".
type innerReportingTool struct{}

func (innerReportingTool) Name() string            { return "echo" }
func (innerReportingTool) Description() string     { return "echoes" }
func (innerReportingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (innerReportingTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	ReportInnerCall(ctx, "read", ToolResult{Content: []provider.Content{provider.TextBlock{Text: "no such file"}}, IsError: true})
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// toolThenText calls the echo tool on the first request and answers in text
// on every later one.
func toolThenText() *scriptedClient {
	return &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return calledATool(100), nil
		}
		return saidText("done", 100), nil
	}}
}

// stepAgent is an agent whose only step gates are the test's own.
func stepAgent(c provider.Client) *Agent {
	return newTestAgent(c, "m", "sys", Registry{"echo": innerReportingTool{}})
}

// A gate is told the prompt began, then shown each tool batch as it ran: the
// assistant message that asked for the call, and the result that answered it.
// A text-only request is not a step.
func TestAStepGateSeesEachToolBatch(t *testing.T) {
	a := stepAgent(toolThenText())
	var begins int
	var steps []StepState
	a.addStepGate(StepGate{
		Cause: "test",
		Begin: func() { begins++ },
		After: func(_ context.Context, s StepState, _ func(AgentEvent)) bool {
			steps = append(steps, s)
			return false
		},
	})
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if begins != 1 {
		t.Errorf("Begin ran %d times for one prompt, want 1", begins)
	}
	if len(steps) != 1 {
		t.Fatalf("After ran %d times, want once for the one tool batch", len(steps))
	}
	tc, ok := steps[0].Assistant.Content[0].(provider.ToolCallBlock)
	if !ok || tc.ID != "call_1" {
		t.Errorf("Assistant = %+v, want the message carrying call_1", steps[0].Assistant)
	}
	tr, ok := steps[0].Results.Content[0].(provider.ToolResultBlock)
	if !ok || tr.CallID != "call_1" {
		t.Errorf("Results = %+v, want call_1's result", steps[0].Results)
	}
}

// Stopping ends the turn cleanly where it stands: no further request, EvDone,
// no error, and the gates after the one that stopped are not asked.
func TestAStepGateCanEndTheTurn(t *testing.T) {
	c := toolThenText()
	a := stepAgent(c)
	later := false
	a.addStepGate(StepGate{After: func(context.Context, StepState, func(AgentEvent)) bool { return true }})
	a.addStepGate(StepGate{After: func(context.Context, StepState, func(AgentEvent)) bool { later = true; return false }})
	done := false
	if err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if _, ok := ev.(EvDone); ok {
			done = true
		}
	}); err != nil {
		t.Fatalf("a stop is a clean end, got %v", err)
	}
	if n := len(c.reqs); n != 1 {
		t.Errorf("%d requests, want 1: the turn went on after a gate stopped it", n)
	}
	if !done {
		t.Error("no EvDone after the gate ended the turn")
	}
	if later {
		t.Error("a gate registered after the one that stopped was still asked")
	}
}

// emit reaches the event observers and then the prompt's sink, which is how an
// event a gate emits becomes a session row and a client update.
func TestAStepGatesEventsReachObserversAndTheSink(t *testing.T) {
	a := stepAgent(toolThenText())
	a.addStepGate(StepGate{After: func(_ context.Context, _ StepState, emit func(AgentEvent)) bool {
		emit(EvStall{StallRecord: StallRecord{Tool: "echo", Rung: 1}})
		return false
	}})
	var observed, sunk int
	a.AddEventObserver(func(ev AgentEvent) {
		if _, ok := ev.(EvStall); ok {
			observed++
		}
	})
	if err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if _, ok := ev.(EvStall); ok {
			sunk++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if observed != 1 || sunk != 1 {
		t.Errorf("the event reached %d observers and the sink %d times, want 1 and 1", observed, sunk)
	}
}

// A tool's report of its own host calls reaches Inner, attributed to the
// model-issued call it was running for.
func TestAnInnerCallReachesTheStepGates(t *testing.T) {
	a := stepAgent(toolThenText())
	type report struct{ outer, tool string }
	var got []report
	a.addStepGate(StepGate{Inner: func(outer, tool string, _ ToolResult) { got = append(got, report{outer, tool}) }})
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (report{"call_1", "read"}) {
		t.Errorf("Inner saw %+v, want the read made for call_1", got)
	}
}

// A gate with nothing to call is not registered.
func TestAnEmptyStepGateIsIgnored(t *testing.T) {
	a := stepAgent(nil)
	a.addStepGate(StepGate{Cause: "nothing"})
	if len(a.stepGateSnapshot()) != 0 {
		t.Error("a gate with no hooks was registered")
	}
}

// addingTool registers a step gate while it runs, then reports an inner call.
type addingTool struct{ add func() }

func (addingTool) Name() string            { return "echo" }
func (addingTool) Description() string     { return "adds a gate" }
func (addingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t addingTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	t.add()
	ReportInnerCall(ctx, "read", ToolResult{IsError: true})
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// A gate added mid-prompt joins at the next prompt in every hook at once. It
// must not hear of an inner call from a step whose After it will not see, nor
// before its Begin has run: its state would describe a step it never saw.
func TestAGateAddedMidPromptHearsNothingUntilTheNext(t *testing.T) {
	c := toolThenText()
	var inner, begins int
	var a *Agent
	added := false
	a = newTestAgent(c, "m", "sys", Registry{"echo": addingTool{add: func() {
		if added {
			return
		}
		added = true
		a.addStepGate(StepGate{
			Begin: func() { begins++ },
			Inner: func(string, string, ToolResult) { inner++ },
		})
	}}})
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("the tool never ran, so this proves nothing")
	}
	if inner != 0 || begins != 0 {
		t.Errorf("a gate added mid-prompt got %d inner calls and %d begins in that prompt, want none", inner, begins)
	}
}

// ReportInnerCall is inert outside a model-issued dispatch. A direct call, a
// test harness, an extension's host_tool_call: none of them have an outer step
// to attribute to, so no gate hears of them, and none of them panic.
func TestReportInnerCallIsInertWithoutADispatch(t *testing.T) {
	res := ToolResult{Content: []provider.Content{provider.TextBlock{Text: "boom"}}, IsError: true}
	heard := 0
	gates := []StepGate{{Inner: func(string, string, ToolResult) { heard++ }}}

	// Nothing in the context at all.
	ReportInnerCall(context.Background(), "read", res)
	// The prompt's gates, but no outer call (the ext host_tool_call door).
	ReportInnerCall(context.WithValue(context.Background(), stepGatesKey{}, gates), "read", res)
	// An outer call, but no gates: a dispatch outside a prompt's loop.
	ReportInnerCall(contextWithOuterCall(context.Background(), "c0"), "read", res)
	if heard != 0 {
		t.Errorf("a report with no model-issued dispatch reached a gate %d times", heard)
	}

	// Positive control: both halves present, and the gate hears it.
	ReportInnerCall(contextWithOuterCall(context.WithValue(context.Background(), stepGatesKey{}, gates), "c0"), "read", res)
	if heard != 1 {
		t.Errorf("a report inside a dispatch reached the gate %d times, want 1", heard)
	}
}
