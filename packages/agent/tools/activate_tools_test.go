package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// activateFakeTool is a grouped tool (has an Extension() accessor) for building
// a lazy registry in these tests. desc overrides the default description so a
// test can inflate a group past the schema-echo budget.
type activateFakeTool struct{ name, group, desc string }

func (f activateFakeTool) Name() string { return f.name }
func (f activateFakeTool) Description() string {
	if f.desc != "" {
		return f.desc
	}
	return "does " + f.name
}
func (f activateFakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"}}}`)
}
func (f activateFakeTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
func (f activateFakeTool) Extension() string { return f.group }

func activateTextOf(r core.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

func TestActivateToolsActivatesGroup(t *testing.T) {
	reg := core.Registry{"mail_send": activateFakeTool{name: "mail_send", group: "mail"}}
	agent := core.NewAgent(nil, "m", "s", reg)
	agent.EnableLazyTools()
	ctx := core.ContextWithAgent(context.Background(), agent)
	tool := &ActivateToolsTool{}

	res, err := tool.Execute(ctx, json.RawMessage(`{"group":"mail"}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", activateTextOf(res))
	}
	if !strings.Contains(activateTextOf(res), "mail_send") {
		t.Errorf("result should name the revealed tools: %q", activateTextOf(res))
	}
	// A small group echoes its schemas so the model can prepare the next call.
	if !strings.Contains(activateTextOf(res), `"properties"`) {
		t.Errorf("result should echo the group's tool schema, got %q", activateTextOf(res))
	}

	// Re-activating reports it was already active (idempotent, not an error) and
	// does NOT re-echo the schemas — they are already in the advertised set.
	res2, _ := tool.Execute(ctx, json.RawMessage(`{"group":"mail"}`), func(string) {})
	if res2.IsError || !strings.Contains(activateTextOf(res2), "already active") {
		t.Errorf("second activation should report already-active, got %q (err=%v)", activateTextOf(res2), res2.IsError)
	}
	if strings.Contains(activateTextOf(res2), `"properties"`) {
		t.Errorf("already-active result should not re-echo schemas: %q", activateTextOf(res2))
	}
}

// A group whose schemas exceed the echo budget falls back to a name-only line
// rather than re-inflating the schema weight lazy tools defers.
func TestActivateToolsSchemaEchoBudget(t *testing.T) {
	big := strings.Repeat("x", groupSchemaEchoBudget) // one tool alone busts the budget
	reg := core.Registry{
		"crm_query": activateFakeTool{name: "crm_query", group: "crm", desc: big},
		"crm_write": activateFakeTool{name: "crm_write", group: "crm", desc: big},
	}
	agent := core.NewAgent(nil, "m", "s", reg)
	agent.EnableLazyTools()
	ctx := core.ContextWithAgent(context.Background(), agent)

	res, _ := (&ActivateToolsTool{}).Execute(ctx, json.RawMessage(`{"group":"crm"}`), func(string) {})
	text := activateTextOf(res)
	if strings.Contains(text, `"properties"`) {
		t.Errorf("over-budget group must not echo schemas: %q", text)
	}
	if !strings.Contains(text, "not echoed here") {
		t.Errorf("over-budget group should say schemas were withheld, got %q", text)
	}
	if !strings.Contains(text, "2 tools") {
		t.Errorf("fallback should report the tool count, got %q", text)
	}
}

func TestActivateToolsUnknownGroupErrors(t *testing.T) {
	reg := core.Registry{"mail_send": activateFakeTool{name: "mail_send", group: "mail"}}
	agent := core.NewAgent(nil, "m", "s", reg)
	agent.EnableLazyTools()
	ctx := core.ContextWithAgent(context.Background(), agent)

	res, _ := (&ActivateToolsTool{}).Execute(ctx, json.RawMessage(`{"group":"nope"}`), func(string) {})
	if !res.IsError {
		t.Error("an unknown group must error")
	}
}

func TestActivateToolsEmptyGroupErrors(t *testing.T) {
	// Empty group is rejected before any agent lookup.
	res, _ := (&ActivateToolsTool{}).Execute(context.Background(), json.RawMessage(`{}`), func(string) {})
	if !res.IsError {
		t.Error("an empty group must error")
	}
}
