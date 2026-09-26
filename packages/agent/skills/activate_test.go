package skills

import (
	"context"
	"encoding/json"
	"strings"
	"terva.sh/terva/packages/core/lazytools"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// skillFakeTool is a grouped tool (has an Extension() accessor) for the agent's
// registry in these tests.
type skillFakeTool struct{ name, group string }

func (f skillFakeTool) Name() string            { return f.name }
func (f skillFakeTool) Description() string     { return "does " + f.name }
func (f skillFakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f skillFakeTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
func (f skillFakeTool) Extension() string { return f.group }

func skillTextOf(r core.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// Loading a skill that declares allowed-tools activates their capability groups
// under lazy tool visibility — visibility only (retro H2·b step 5).
func TestSkillActivatesAllowedToolGroups(t *testing.T) {
	reg := core.Registry{"mail_send": skillFakeTool{"mail_send", "mail"}}
	agent := coretest.NewAgent(nil, "m", "s", reg, core.WithComponent(lazytools.New()))
	ctx := core.ContextWithAgent(context.Background(), agent)

	tool := NewTool([]*Skill{{
		Name:         "compose-mail",
		Description:  "compose an email",
		Body:         "steps",
		AllowedTools: []string{"mail_send"},
	}})

	res, err := tool.Execute(ctx, json.RawMessage(`{"name":"compose-mail"}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %q", skillTextOf(res))
	}
	if txt := skillTextOf(res); !strings.Contains(txt, "Activated") || !strings.Contains(txt, "mail") {
		t.Errorf("skill result should note the activated group: %q", txt)
	}
	// The group is now active: a fresh activation reports no change.
	if lazytools.Of(agent).Activate("mail") {
		t.Error("mail should already be active after the skill loaded it")
	}
}

// A skill whose allowed-tools are absent from the registry (e.g. an untrusted
// workspace never loaded that extension) activates nothing — the untrusted-
// workspace safety half of the §Security gate.
func TestSkillAllowedToolsAbsentNoActivation(t *testing.T) {
	agent := coretest.NewAgent(nil, "m", "s", core.Registry{}, core.WithComponent(lazytools.New()))
	ctx := core.ContextWithAgent(context.Background(), agent)

	tool := NewTool([]*Skill{{Name: "x", Body: "b", AllowedTools: []string{"mail_send"}}})
	res, _ := tool.Execute(ctx, json.RawMessage(`{"name":"x"}`), func(string) {})
	if strings.Contains(skillTextOf(res), "Activated") {
		t.Error("absent allowed-tools must activate nothing (untrusted-workspace safety)")
	}
}
