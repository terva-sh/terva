package build

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
)

// plainTool is a built-in-like tool with no Extension() accessor.
type plainTool struct{ name string }

func (p plainTool) Name() string            { return p.name }
func (p plainTool) Description() string     { return "d" }
func (p plainTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (p plainTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

// groupedTool exposes an Extension() accessor, like an extension or MCP tool.
type groupedTool struct {
	plainTool
	group string
}

func (g groupedTool) Extension() string { return g.group }

func TestToolGroupClassifies(t *testing.T) {
	tests := []struct {
		name string
		tool core.Tool
		want string
	}{
		{"builtin has no accessor -> core", plainTool{name: "read"}, CoreToolGroup},
		{"extension tool -> its name", groupedTool{plainTool{name: "mail_send"}, "mail"}, "mail"},
		{"mcp tool -> mcp:server", groupedTool{plainTool{name: "gh_pr"}, "mcp:github"}, "mcp:github"},
		{"empty accessor falls back to core", groupedTool{plainTool{name: "x"}, ""}, CoreToolGroup},
	}
	for _, tc := range tests {
		if got := ToolGroup(tc.tool); got != tc.want {
			t.Errorf("%s: ToolGroup = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestToolGroupsMap(t *testing.T) {
	reg := core.Registry{
		"read":      plainTool{name: "read"},
		"mail_send": groupedTool{plainTool{name: "mail_send"}, "mail"},
		"gh_pr":     groupedTool{plainTool{name: "gh_pr"}, "mcp:github"},
	}
	got := ToolGroups(reg)
	want := map[string]string{"read": "core", "mail_send": "mail", "gh_pr": "mcp:github"}
	if len(got) != len(want) {
		t.Fatalf("ToolGroups = %v, want %v", got, want)
	}
	for name, g := range want {
		if got[name] != g {
			t.Errorf("ToolGroups[%q] = %q, want %q", name, got[name], g)
		}
	}
}
