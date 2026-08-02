package core

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The defect this closes: an invalid json.RawMessage does not fail politely at
// the field, it makes json.Marshal of the WHOLE message return zero bytes. The
// assistant turn then never reaches disk while its tool_result does, leaving an
// orphan result — which is exactly what the session that prompted this holds.
//
// ToolCallBlock is built outside the provider package too (the SDK, replay,
// tests), so the writer must hold this on its own rather than trusting every
// producer.
func TestATurnWithUnwritableArgumentsStillReachesDisk(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/cwd", "anthropic", "claude-opus-5", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}

	// A raw tab inside a JSON string: unparseable, and fatal to json.Marshal.
	broken := "{\"path\":\"a.go\",\"edits\":[{\"oldText\":\"func f() {\n\treturn\n}\"}]}"
	if json.Valid([]byte(broken)) {
		t.Fatal("fixture is valid JSON — it no longer reproduces the write failure")
	}

	msg := provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.Content{
			provider.TextBlock{Text: "editing"},
			provider.ToolCallBlock{ID: "toolu_x", Name: "edit", Arguments: json.RawMessage(broken)},
		},
	}
	if err := sess.AppendMessage(msg); err != nil {
		t.Fatalf("the turn was lost: %v", err)
	}

	raw, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	// The call must be attributable after the fact: which tool, which id.
	for _, want := range []string{"toolu_x", `"edit"`, "editing"} {
		if !strings.Contains(body, want) {
			t.Errorf("row does not carry %s:\n%s", want, body)
		}
	}
	// And the model's original text must survive, since it is the only record
	// of what was attempted.
	if !strings.Contains(body, "raw_arguments") {
		t.Errorf("the unparseable text was dropped instead of preserved:\n%s", body)
	}

	// Round-trip: what comes back must be usable, and must still be flagged.
	_, msgs, err := OpenSession(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	var call provider.ToolCallBlock
	for _, m := range msgs {
		for _, c := range m.Content {
			if tc, ok := c.(provider.ToolCallBlock); ok {
				call = tc
			}
		}
	}
	if call.ID != "toolu_x" {
		t.Fatalf("the tool call did not survive the round trip: %+v", msgs)
	}
	if !json.Valid(call.Arguments) {
		t.Errorf("reloaded Arguments are not valid JSON: %s", call.Arguments)
	}
	if call.RawArguments == "" {
		t.Errorf("reloaded block lost the unparseable text, so nothing marks it as broken")
	}
}

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
	ag := &Agent{}
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
	ag := &Agent{}
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
