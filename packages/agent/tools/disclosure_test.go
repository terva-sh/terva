//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// execDisclosure runs a script against a read-only tool wired to a fake
// host and a catalog, the way WireHostToolDispatcher wires it in a session.
func execDisclosure(t *testing.T, host *fakeHost, catalog *DisclosureCatalog, script string) core.ToolResult {
	t.Helper()
	tool := &CodeExecutionTool{HostCall: host.call, Catalog: catalog}
	args, err := json.Marshal(map[string]any{"script": script})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// metaEntry stands in for one curated meta builtin in the catalog.
func metaEntry(name string) CatalogEntry {
	return CatalogEntry{
		Name:        name,
		Description: "a meta tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Source:      "builtin",
	}
}

// pluginEntry stands in for an authority-matched extension or MCP tool.
func pluginEntry(name string) CatalogEntry {
	return CatalogEntry{
		Name:        name,
		Description: "a plugin tool",
		Schema:      json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		Source:      "mcp:test-server",
	}
}

func TestDisclosureCatalogListOmitsSchema(t *testing.T) {
	cat := NewDisclosureCatalog([]CatalogEntry{metaEntry("task_list"), pluginEntry("search")})
	out := cat.List()
	if !strings.Contains(out, `"name": "task_list"`) || !strings.Contains(out, `"name": "search"`) {
		t.Fatalf("List missing entries: %s", out)
	}
	if strings.Contains(out, `"schema"`) {
		t.Errorf("List must omit schemas so a listing stays cheap, got: %s", out)
	}
	if !strings.Contains(out, `"source": "mcp:test-server"`) {
		t.Errorf("List must carry provenance, got: %s", out)
	}
}

func TestDisclosureDescribeReturnsTheSchema(t *testing.T) {
	cat := NewDisclosureCatalog([]CatalogEntry{pluginEntry("search")})
	e, err := cat.Describe("search")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(string(e.Schema), `"q"`) {
		t.Errorf("Describe must return the real schema, got %s", e.Schema)
	}
	if _, err := cat.Describe("not-there"); err == nil {
		t.Error("Describe of an undisclosed name must fail")
	}
}

// tools() and describe() run in-engine: no host call, no approval gate.
func TestDisclosureListingCostsNoHostCall(t *testing.T) {
	host := &fakeHost{text: map[string]string{}}
	cat := NewDisclosureCatalog([]CatalogEntry{metaEntry("task_list")})
	res := execDisclosure(t, host, cat, `print(tools()); print(describe("task_list"))`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", ceResultText(res))
	}
	if len(host.calls) != 0 {
		t.Errorf("listing must not dispatch to the host, got %v", host.calls)
	}
}

// call() dispatches through HostCall — the same gate a fixed binding uses.
func TestDisclosureCallDispatchesThroughTheGate(t *testing.T) {
	host := &fakeHost{text: map[string]string{"search": "found it"}}
	cat := NewDisclosureCatalog([]CatalogEntry{pluginEntry("search")})
	res := execDisclosure(t, host, cat, `print(call("search", {q: "needle"}))`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", ceResultText(res))
	}
	if len(host.calls) != 1 || !strings.Contains(host.calls[0], "search") {
		t.Fatalf("call() must reach the host once, got %v", host.calls)
	}
	if !strings.Contains(ceResultText(res), "found it") {
		t.Errorf("result should carry the tool's output, got %q", ceResultText(res))
	}
}

// call() refuses a name outside the catalog — the authority boundary.
func TestDisclosureCallRefusesAnUndisclosedTool(t *testing.T) {
	host := &fakeHost{text: map[string]string{"bash": "should never run"}}
	cat := NewDisclosureCatalog([]CatalogEntry{pluginEntry("search")})
	res := execDisclosure(t, host, cat, `call("bash", {})`)
	if !res.IsError {
		t.Fatal("call to an undisclosed tool must fail")
	}
	if len(host.calls) != 0 {
		t.Errorf("a refused call must not reach the host, got %v", host.calls)
	}
	if !strings.Contains(ceResultText(res), "not disclosed") {
		t.Errorf("refusal should say why, got %q", ceResultText(res))
	}
}

// call() with a computed name is refused at runtime AND defeats the account.
func TestDisclosureCallRejectsAComputedName(t *testing.T) {
	host := &fakeHost{text: map[string]string{}}
	cat := NewDisclosureCatalog([]CatalogEntry{pluginEntry("search")})
	res := execDisclosure(t, host, cat, `const n = "sea" + "rch"; call(n, {})`)
	if !res.IsError {
		t.Fatal("call with a computed name must fail")
	}
	if len(host.calls) != 0 {
		t.Errorf("a computed-name call must not reach the host, got %v", host.calls)
	}
}
