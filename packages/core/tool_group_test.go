package core

import (
	"context"
	"encoding/json"
	"testing"
)

type tgPlainTool struct{}

func (tgPlainTool) Name() string            { return "plain" }
func (tgPlainTool) Description() string     { return "plain" }
func (tgPlainTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (tgPlainTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	return ToolResult{}, nil
}

type tgExtTool struct{ tgPlainTool }

func (tgExtTool) Extension() string { return "weather" }

type tgGroupedTool struct{ tgPlainTool }

func (tgGroupedTool) ToolGroupName() string { return "scripting" }

// A tool carrying both accessors: the explicit classification wins over
// the provenance fact.
type tgGroupedExtTool struct{ tgExtTool }

func (tgGroupedExtTool) ToolGroupName() string { return "scripting" }

// tgEssentialTool is an extension tool that declares itself load-bearing.
type tgEssentialTool struct{ tgExtTool }

func (tgEssentialTool) Essential() bool { return true }

func TestToolEssential(t *testing.T) {
	if ToolEssential(tgPlainTool{}) {
		t.Error("a plain built-in is never essential")
	}
	if ToolEssential(tgExtTool{}) {
		t.Error("an extension tool without the accessor is not essential")
	}
	if !ToolEssential(tgEssentialTool{}) {
		t.Error("a tool declaring Essential() true must be essential")
	}
}

func TestToolGroupClassification(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		want string
	}{
		{"builtin defaults to core", tgPlainTool{}, CoreToolGroup},
		{"extension reports its name", tgExtTool{}, "weather"},
		{"builtin may opt into a named group", tgGroupedTool{}, "scripting"},
		{"explicit group beats provenance", tgGroupedExtTool{}, "scripting"},
	}
	for _, c := range cases {
		if got := ToolGroup(c.tool); got != c.want {
			t.Errorf("%s: ToolGroup = %q, want %q", c.name, got, c.want)
		}
	}
}
