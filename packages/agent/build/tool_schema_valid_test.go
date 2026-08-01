package build

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// Every tool schema in this tree is a hand-written JSON literal built by
// string concatenation, which has one failure mode that costs an hour
// each time: a description containing a double quote lands inside a JSON
// string and invalidates the whole document.
//
// Nothing caught that near the tool. The provider marshals the schema, so
// the first symptom is an error frame four layers away —
//
//	json: error calling MarshalJSON for type json.RawMessage:
//	invalid character 'a' after object key:value pair
//
// — which names neither the tool nor the field, and shows up as a dozen
// unrelated e2e failures. This is the cheap check that names it: every
// registered tool's schema must parse, and must be an object with
// properties, which is what a provider expects to receive.
func TestEveryToolSchemaIsValidJSON(t *testing.T) {
	reg := BuildToolRegistry(
		Args{CWD: testsupport.TempDir(t)},
		core.ApprovalYolo,
		testsupport.TempDir(t),
		nil, "anthropic", "api-key", false, nil,
	)
	if len(reg) == 0 {
		t.Fatal("empty registry — this guard would pass by testing nothing")
	}
	for _, tool := range reg {
		raw := tool.Schema()
		if len(raw) == 0 {
			continue // a tool may legitimately take no arguments
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v\n%s", tool.Name(), err, raw)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: schema type is %v, want object", tool.Name(), schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%s: schema has no properties", tool.Name())
		}
	}
}
