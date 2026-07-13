package workspace

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// ctxFakeTool is a tool whose Extension() places it in a capability group; ""
// means a built-in (core group).
type ctxFakeTool struct{ name, ext string }

func (f ctxFakeTool) Name() string            { return f.name }
func (f ctxFakeTool) Description() string     { return "does " + f.name }
func (f ctxFakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f ctxFakeTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
func (f ctxFakeTool) Extension() string { return f.ext }

// With only built-ins, the /context tool node stays a flat per-tool list — no
// needless "core" wrapper for the common (no-extension) case.
func TestCtxToolsNodeFlatWhenOnlyCore(t *testing.T) {
	reg := core.Registry{
		"read": ctxFakeTool{name: "read"},
		"bash": ctxFakeTool{name: "bash"},
	}
	node := ctxToolsNode(reg, nil, false)
	if len(node.Children) != 2 {
		t.Fatalf("flat tools node: got %d children, want 2", len(node.Children))
	}
	for _, c := range node.Children {
		if c.Kind != "tool" {
			t.Errorf("want flat tool leaves, got kind %q", c.Kind)
		}
	}
}

// With extensions/MCP present, tools bucket under per-group wrappers (core
// first) each carrying a byte rollup — the operator's per-group cost view.
func TestCtxToolsNodeGroupsWithExtensions(t *testing.T) {
	reg := core.Registry{
		"read":      ctxFakeTool{name: "read"},
		"mail_send": ctxFakeTool{name: "mail_send", ext: "mail"},
		"gh_pr":     ctxFakeTool{name: "gh_pr", ext: "mcp:github"},
	}
	node := ctxToolsNode(reg, nil, false)
	if len(node.Children) != 3 {
		t.Fatalf("grouped tools node: got %d group children, want 3", len(node.Children))
	}
	if node.Children[0].Label != build.CoreToolGroup {
		t.Errorf("core group must render first, got %q", node.Children[0].Label)
	}
	sum := 0
	for _, g := range node.Children {
		if g.Kind != "group" {
			t.Errorf("want a group wrapper, got kind %q", g.Kind)
		}
		if g.Bytes <= 0 || len(g.Children) == 0 {
			t.Errorf("group %q must carry a byte rollup and children", g.Label)
		}
		if g.Meta["group"] != g.Label {
			t.Errorf("group node meta = %v, want group=%q", g.Meta, g.Label)
		}
		sum += g.Bytes
	}
	if node.Bytes != sum {
		t.Errorf("section bytes %d != sum of group bytes %d", node.Bytes, sum)
	}
}

// Under lazy visibility the section reports the LIVE advertised weight, not the
// full installed registry: an inactive group contributes 0 live bytes (only its
// names ride the ephemeral note) and is marked not-loaded with its deferred
// weight, while the section Meta carries the advertised-vs-installed split.
func TestCtxToolsNodeLazyAdvertisedSplit(t *testing.T) {
	reg := core.Registry{
		"read":      ctxFakeTool{name: "read"},
		"mail_send": ctxFakeTool{name: "mail_send", ext: "mail"},
		"gh_pr":     ctxFakeTool{name: "gh_pr", ext: "mcp:github"},
	}
	// Lazy mode with only "mail" active: core advertised, mail advertised,
	// mcp:github hidden.
	advertised := func(name string) bool { return name == "read" || name == "mail_send" }
	node := ctxToolsNode(reg, advertised, true)

	var live, installed int
	byLabel := map[string]ctrlproto.ContextNode{}
	for _, g := range node.Children {
		byLabel[g.Label] = g
		live += g.Bytes
	}
	for _, spec := range reg.Specs() {
		raw, _ := json.MarshalIndent(spec, "", "  ")
		installed += len(raw)
	}
	if node.Bytes != live {
		t.Errorf("section live bytes %d != sum of group live bytes %d", node.Bytes, live)
	}
	if node.Bytes >= installed {
		t.Errorf("advertised weight %d should be below installed weight %d", node.Bytes, installed)
	}
	if gh := byLabel["mcp:github"]; gh.Bytes != 0 || gh.Meta["active"] != "false" {
		t.Errorf("inactive mcp:github group should be 0 live bytes and active=false, got bytes=%d meta=%v", gh.Bytes, gh.Meta)
	}
	if gh := byLabel["mcp:github"]; gh.Meta["deferred_bytes"] == "" {
		t.Errorf("inactive group should report deferred_bytes, got %v", gh.Meta)
	}
	if m := byLabel["mail"]; m.Bytes <= 0 || m.Meta["active"] != "true" {
		t.Errorf("active mail group should carry live bytes and active=true, got bytes=%d meta=%v", m.Bytes, m.Meta)
	}
	if node.Meta["installed_tools"] != "3" || node.Meta["advertised_tools"] != "2" {
		t.Errorf("section meta should report 2 of 3 advertised, got %v", node.Meta)
	}
}
