//go:build !terva_no_mcp_http

package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A tool whose description or schema is over its limit is withdrawn from every
// view the manager offers, with a warning, and its neighbours stay.
func TestMCPOversizedToolMetadataIsWithdrawn(t *testing.T) {
	// A schema that is over the limit only because of its indentation: the
	// limit measures what a provider sends, so this one stays.
	indented := `{` + strings.Repeat(" ", 2*maxToolSchemaBytes) + `"type":"object"}`
	sprawl := fmt.Sprintf(`{"type":"object","properties":{"x":{"type":"string","description":%q}}}`, strings.Repeat("s", maxToolSchemaBytes))
	list := fmt.Sprintf(`{"tools":[
		{"name":"ok","description":"fine","inputSchema":{"type":"object"}},
		{"name":"wordy","description":%q,"inputSchema":{"type":"object"}},
		{"name":"sprawl","description":"big schema","inputSchema":%s},
		{"name":"indented","description":"roomy","inputSchema":%s}
	]}`, strings.Repeat("d", maxToolDescriptionBytes+1), sprawl, indented)
	m := startCapFixture(t, list, `{"content":[{"type":"text","text":"called"}],"isError":false}`)

	var names []string
	for _, info := range m.Tools() {
		names = append(names, info.Name)
	}
	if strings.Join(names, ",") != "mcp_fixture_ok,mcp_fixture_indented" {
		t.Fatalf("tools = %v, want ok and indented only", names)
	}
	st := m.Status()
	if len(st) != 1 || st[0].ToolCount != 2 {
		t.Fatalf("status = %+v, want one server with 2 tools", st)
	}

	// The "mcp server <name>: " prefix is what the /mcp dialog splits on to
	// show the reason against its server.
	warns := strings.Join(m.Warnings(), "\n")
	for _, want := range []string{
		fmt.Sprintf("mcp server fixture: tool wordy withdrawn: its description is %d bytes, over the %d-byte limit", maxToolDescriptionBytes+1, maxToolDescriptionBytes),
		"mcp server fixture: tool sprawl withdrawn: its input schema is ",
	} {
		if !strings.Contains(warns, want) {
			t.Errorf("warnings lack %q:\n%s", want, warns)
		}
	}
	if strings.Contains(warns, "indented") || strings.Contains(warns, "tool ok") {
		t.Errorf("a tool within its limits drew a warning:\n%s", warns)
	}

	for _, ns := range []string{"mcp_fixture_wordy", "mcp_fixture_sprawl"} {
		if _, err := m.Call(context.Background(), ns, nil); err == nil {
			t.Errorf("Call(%s) reached a withdrawn tool", ns)
		}
		if _, err := m.NewTool(ToolInfo{Name: ns}).Execute(context.Background(), nil, nil); err == nil {
			t.Errorf("NewTool(%s) reached a withdrawn tool", ns)
		}
	}
	if res, err := m.Call(context.Background(), "mcp_fixture_ok", nil); err != nil || len(res.Content) != 1 {
		t.Fatalf("the normal tool failed beside the withdrawn ones: %+v %v", res, err)
	}
}

// Two of one server's names can clean to the same namespace. When the one that
// is withdrawn comes first, the survivor holds the route, and Tools must not
// list the withdrawn one under it.
func TestMCPWithdrawnToolDoesNotLeakThroughASharedNamespace(t *testing.T) {
	list := fmt.Sprintf(`{"tools":[
		{"name":"a.b","description":%q,"inputSchema":{"type":"object"}},
		{"name":"a_b","description":"fine","inputSchema":{"type":"object"}}
	]}`, strings.Repeat("d", maxToolDescriptionBytes+1))
	m := startCapFixture(t, list, `{"content":[],"isError":false}`)
	var listed []string
	for _, info := range m.Tools() {
		listed = append(listed, info.wireName)
	}
	if strings.Join(listed, ",") != "a_b" {
		t.Fatalf("tools = %v, want a_b alone", listed)
	}
}
