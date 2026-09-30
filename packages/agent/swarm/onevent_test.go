package swarm

import (
	"slices"
	"testing"
)

// Every event a runner ingests reaches the agent's OnEvent, in order, and a
// cleared hook hears nothing.
func TestIngestEventFiresOnEventInOrder(t *testing.T) {
	a := &Agent{ID: "w-1"}
	sink := agentSink{a: a}
	var got []string
	a.SetOnEvent(func(ev Event) { got = append(got, ev.Type) })
	for _, typ := range []string{"turn_start", "tool_call", "tool_result", "assistant_message"} {
		IngestEvent(NewEvent(typ, map[string]any{}), nil, sink, a)
	}
	want := []string{"turn_start", "tool_call", "tool_result", "assistant_message"}
	if !slices.Equal(got, want) {
		t.Errorf("OnEvent heard %v, want %v", got, want)
	}
	a.SetOnEvent(nil)
	IngestEvent(NewEvent("turn_start", map[string]any{}), nil, sink, a)
	if len(got) != len(want) {
		t.Errorf("a cleared OnEvent still heard %v", got[len(want):])
	}
}
