package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeTool is a minimal core.Tool whose advertised weight (name + description +
// schema) is entirely under the test's control.
type fakeTool struct {
	name, desc string
	schema     string
}

func (f fakeTool) Name() string            { return f.name }
func (f fakeTool) Description() string     { return f.desc }
func (f fakeTool) Schema() json.RawMessage { return json.RawMessage(f.schema) }
func (f fakeTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

func TestBuildPromptSizes_Attribution(t *testing.T) {
	heavy := fakeTool{name: "heavy", desc: "a big tool", schema: strings.Repeat("x", 400)}
	light := fakeTool{name: "light", desc: "small", schema: "{}"}
	r := &Resolved{
		SystemSegments: []PromptSegment{
			{Source: "identity-intro", Text: "You are X."}, // 10 bytes
			{Source: "agents-md", Text: strings.Repeat("a", 100)},
		},
		ToolRegistry: core.Registry{"heavy": heavy, "light": light},
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
	}
	s := r.BuildPromptSizes(msgs)

	// System + messages rows carry exact byte lengths and bytes/4 tokens.
	sys := rowBySource(s.Rows, "identity-intro")
	if sys == nil || sys.Section != sectionSystem || sys.Bytes != len("You are X.") || sys.Tokens != len("You are X.")/4 {
		t.Fatalf("identity-intro row wrong: %+v", sys)
	}
	if msg := rowBySource(s.Rows, "user"); msg == nil || msg.Section != sectionMessages || msg.Bytes != 2 {
		t.Fatalf("user message row wrong: %+v", msg)
	}

	// Tool rows attribute name+description+schema and sort heaviest first.
	hv := rowBySource(s.Rows, "tool:heavy")
	lt := rowBySource(s.Rows, "tool:light")
	if hv == nil || lt == nil {
		t.Fatalf("missing tool rows: %+v", s.Rows)
	}
	if want := len("heavy") + len("a big tool") + 400; hv.Bytes != want {
		t.Errorf("heavy bytes = %d, want %d", hv.Bytes, want)
	}
	if hv.Detail != "a big tool" {
		t.Errorf("heavy detail = %q", hv.Detail)
	}
	if idx(s.Rows, "tool:heavy") > idx(s.Rows, "tool:light") {
		t.Errorf("tools not sorted heaviest-first: %+v", toolOrder(s.Rows))
	}
}

func TestPromptSizes_Text(t *testing.T) {
	r := &Resolved{
		SystemSegments: []PromptSegment{{Source: "agents-md", Text: strings.Repeat("a", 40)}},
		ToolRegistry:   core.Registry{"read": fakeTool{name: "read", desc: "read a file", schema: "{}"}},
	}
	out := r.BuildPromptSizes(nil).Text()
	for _, want := range []string{"PROMPT COMPOSITION", "SECTION", "system", "tools", "TOTAL", "by weight", "by source"} {
		if !strings.Contains(out, want) {
			t.Errorf("Text() missing %q:\n%s", want, out)
		}
	}
	// A section with no content must not appear (empty tail is omitted, not "0").
	if strings.Contains(out, sectionTail) {
		t.Errorf("empty tail section should be omitted:\n%s", out)
	}
}

func rowBySource(rows []PromptSizeRow, src string) *PromptSizeRow {
	for i := range rows {
		if rows[i].Source == src {
			return &rows[i]
		}
	}
	return nil
}

func idx(rows []PromptSizeRow, src string) int {
	for i := range rows {
		if rows[i].Source == src {
			return i
		}
	}
	return -1
}

func toolOrder(rows []PromptSizeRow) []string {
	var out []string
	for _, r := range rows {
		if r.Section == sectionTools {
			out = append(out, r.Source)
		}
	}
	return out
}
