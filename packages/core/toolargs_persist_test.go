package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// unparseableProbeTool records whether it was reached at all.
type unparseableProbeTool struct{ ran bool }

func (t *unparseableProbeTool) Name() string            { return "edit" }
func (t *unparseableProbeTool) Description() string     { return "probe" }
func (t *unparseableProbeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *unparseableProbeTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (ToolResult, error) {
	t.ran = true
	return ToolResult{}, nil
}

// Tested through executeTools rather than against the message helper, because
// the helper being correct says nothing about whether dispatch consults it.
//
// The tool must NOT run. Handing it the "{}" placeholder would make it fail on
// a missing required field, which blames the wrong thing entirely — the model
// would go looking for an argument it did supply.
func TestAnUnparseableCallIsRefusedWithoutRunningTheTool(t *testing.T) {
	tool := &unparseableProbeTool{}
	ag := &Agent{gate: AllowAll}
	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{
			ID: "toolu_x", Name: "edit",
			Arguments:    json.RawMessage(`{}`),
			RawArguments: "{\"path\":\"a.go\",\"edits\":[{\"oldText\":\"x\ty\"}]}",
		},
	}}

	out, hadError := ag.executeTools(context.Background(), msg, Registry{"edit": tool}, func(AgentEvent) {})

	if tool.ran {
		t.Error("the tool ran on the {} placeholder instead of the call being refused")
	}
	if !hadError {
		t.Error("an unrunnable call was reported as a success")
	}
	var text string
	for _, c := range out.Content {
		if tr, ok := c.(provider.ToolResultBlock); ok {
			text += resultText(ToolResult{Content: tr.Content})
		}
	}
	if !strings.Contains(strings.ToLower(text), "tab") {
		t.Errorf("the refusal does not name the defect:\n%s", text)
	}
}

// The ordinary path must be untouched — a guard that refuses everything would
// pass the test above for the worst possible reason.
func TestAnOrdinaryCallStillReachesItsTool(t *testing.T) {
	tool := &unparseableProbeTool{}
	ag := &Agent{gate: AllowAll}
	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "toolu_y", Name: "edit", Arguments: json.RawMessage(`{"path":"a.go"}`)},
	}}

	if _, hadError := ag.executeTools(context.Background(), msg, Registry{"edit": tool}, func(AgentEvent) {}); hadError {
		t.Error("a well-formed call was reported as an error")
	}
	if !tool.ran {
		t.Error("a well-formed call never reached its tool")
	}
}
