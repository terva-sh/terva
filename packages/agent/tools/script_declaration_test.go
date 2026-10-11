//go:build terva_scripting

package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScriptDeclarationSchemaTypes(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{
		"query":{"type":"string"},"limit":{"type":"integer"},
		"mode":{"enum":["fast","thorough"]},
		"filters":{"type":"array","items":{"type":"object","properties":{"active":{"type":"boolean"}},"required":["active"],"additionalProperties":false}},
		"choice":{"anyOf":[{"type":"string"},{"type":"null"}]},
		"quoted-key":{"const":"a\"b"}},"required":["query","filters"],"additionalProperties":false}`)
	got := scriptToolDeclaration("mcp_fixture_search", raw)
	for _, want := range []string{`name: "mcp_fixture_search"`, `"query": string;`, `"limit"?: number;`, `"mode"?: ("fast" | "thorough");`, `"filters": Array<{ "active": boolean; }>;`, `"choice"?: (string | null);`, `"quoted-key"?: "a\"b";`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "incomplete") {
		t.Fatalf("supported schema marked incomplete: %s", got)
	}
}

func TestScriptDeclarationFallbacksAndBounds(t *testing.T) {
	for _, raw := range []string{`{`, `{"$ref":"#/definitions/Input"}`, `{"const":3,"not":{"type":"number"}}`, `{"type":"array","prefixItems":[{"type":"string"}]}`, strings.Repeat(" ", 65<<10)} {
		got := scriptToolDeclaration("mcp_fixture_search", json.RawMessage(raw))
		if !strings.Contains(got, "unknown") || !strings.Contains(got, "incomplete") || len(got) > maxScriptDeclarationBytes {
			t.Fatalf("unbounded or misleading fallback: %s", got)
		}
	}
	deep := `{"type":"string"}`
	for i := 0; i < 20; i++ {
		deep = `{"type":"array","items":` + deep + `}`
	}
	if got := scriptToolDeclaration("nested", json.RawMessage(deep)); !strings.Contains(got, "incomplete") || len(got) > maxScriptDeclarationBytes {
		t.Fatal("depth was not bounded")
	}
	props := map[string]any{}
	for i := 0; i < 100; i++ {
		props[strings.Repeat("p", 90)+strings.Repeat("x", i)] = map[string]any{"type": "string"}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	got := scriptToolDeclaration("wide", raw)
	if len(got) > maxScriptDeclarationBytes || !strings.Contains(got, "args: unknown") {
		t.Fatal("declaration size cap did not replace oversized type")
	}
}

func TestScriptDeclarationNamespacesAndDisclosure(t *testing.T) {
	name := "mcp_fixture_tool-with-dash\"quoted"
	cat := NewDisclosureCatalog([]CatalogEntry{{Name: name, Schema: json.RawMessage(`{"type":"object"}`), Source: "mcp:fixture"}})
	if strings.Contains(cat.List(), "declaration") {
		t.Fatal("listing eagerly exposes declarations")
	}
	e, err := cat.Describe(name)
	if err != nil || !strings.Contains(e.Declaration, `name: "mcp_fixture_tool-with-dash\"quoted"`) {
		t.Fatalf("tool namespace or escaping lost: %+v, %v", e, err)
	}
	if _, err := cat.Describe("bash"); err == nil {
		t.Fatal("undisclosed tool got a declaration")
	}
	if _, err := (*DisclosureCatalog)(nil).Describe(name); err == nil {
		t.Fatal("unwired catalog exposed types")
	}
	host := &fakeHost{text: map[string]string{}}
	res := execDisclosure(t, host, cat, `const d=JSON.parse(describe("mcp_fixture_tool-with-dash\"quoted"));print(d.declaration);`)
	if res.IsError || len(host.calls) != 0 || !strings.Contains(ceResultText(res), "declare function call") {
		t.Fatalf("describe did not expose declaration in-engine: %s", ceResultText(res))
	}
}

func TestScriptDeclarationCompositeSchemas(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{`{"type":["string","null"]}`, `args: (string | null)`},
		{`{"oneOf":[{"const":true},{"const":false}]}`, `args: (true | false)`},
		{`{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false},{"type":"object","properties":{"b":{"type":"number"}},"additionalProperties":false}]}`, `"a": string; } & { "b"?: number; }`},
		{`{"type":"object","additionalProperties":{"type":"number"}}`, `[key: string]: number;`},
		{`{"type":"object","properties":{"base":{"type":"string"}},"required":["base"],"allOf":[{"type":"object","properties":{"extra":{"type":"number"}}}]}`, `"base": string;`},
	} {
		if got := scriptToolDeclaration("fixture", json.RawMessage(tc.schema)); !strings.Contains(got, tc.want) {
			t.Fatalf("missing %s: %s", tc.want, got)
		}
	}
}

func TestScriptDeclarationClosedAndPatternedObjects(t *testing.T) {
	closed := scriptToolDeclaration("empty", json.RawMessage(`{"type":"object","additionalProperties":false}`))
	if !strings.Contains(closed, "args: Record<string, never>") || strings.Contains(closed, "incomplete") {
		t.Fatalf("closed empty object lost its shape: %s", closed)
	}
	patterned := scriptToolDeclaration("patterned", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"patternProperties":{"^x_":{"type":"number"}},"additionalProperties":false}`))
	for _, want := range []string{`"id": string;`, `[key: string]: unknown;`, "incomplete"} {
		if !strings.Contains(patterned, want) {
			t.Fatalf("pattern fallback excludes valid fields: %s", patterned)
		}
	}
}

func TestScriptDeclarationUntypedStructuralKeywords(t *testing.T) {
	for _, schema := range []string{
		`{"properties":{"id":{"type":"string"}},"required":["id"]}`,
		`{"items":{"type":"number"}}`,
		`{"additionalProperties":false}`,
	} {
		got := scriptToolDeclaration("untyped", json.RawMessage(schema))
		if !strings.Contains(got, "args: unknown") || !strings.Contains(got, "incomplete") {
			t.Fatalf("untyped schema excludes valid other types: %s", got)
		}
	}
}
