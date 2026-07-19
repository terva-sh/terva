package deliverable

import (
	"encoding/json"
	"strings"
	"testing"
)

const findingsSchema = `{
	"type": "object",
	"required": ["summary", "findings"],
	"additionalProperties": false,
	"properties": {
		"summary": {"type": "string"},
		"findings": {
			"type": "array",
			"items": {
				"type": "object",
				"required": ["file", "severity"],
				"properties": {
					"file": {"type": "string"},
					"line": {"type": "integer"},
					"severity": {"enum": ["low", "medium", "high"]}
				}
			}
		}
	}
}`

func TestValidateAccepts(t *testing.T) {
	doc := `{"summary":"ok","findings":[{"file":"a.go","line":3,"severity":"high"}]}`
	if err := Validate(json.RawMessage(findingsSchema), json.RawMessage(doc)); err != nil {
		t.Fatalf("valid doc rejected: %v", err)
	}
}

func TestValidateErrorsCarryPaths(t *testing.T) {
	cases := []struct {
		name, doc, wantPath string
	}{
		{"missing required", `{"summary":"ok"}`, `$: missing required property "findings"`},
		{"wrong nested type", `{"summary":"ok","findings":[{"file":7,"severity":"low"}]}`, `$.findings[0].file: expected string`},
		{"non-integer line", `{"summary":"ok","findings":[{"file":"a","line":1.5,"severity":"low"}]}`, `$.findings[0].line: expected integer`},
		{"enum miss", `{"summary":"ok","findings":[{"file":"a","severity":"apocalyptic"}]}`, `$.findings[0].severity: value not in enum`},
		{"additional property", `{"summary":"ok","findings":[],"extra":1}`, `unexpected property "extra"`},
		{"top-level type", `[1,2]`, `$: expected object, got array`},
	}
	for _, c := range cases {
		err := Validate(json.RawMessage(findingsSchema), json.RawMessage(c.doc))
		if err == nil {
			t.Errorf("%s: accepted, want error containing %q", c.name, c.wantPath)
			continue
		}
		if !strings.Contains(err.Error(), c.wantPath) {
			t.Errorf("%s: err = %q, want it to contain %q", c.name, err, c.wantPath)
		}
	}
}

func TestValidateSubsetPermissiveness(t *testing.T) {
	// Unknown keywords and type names constrain nothing.
	schema := `{"type":"object","format":"custom","x-vendor":true,"properties":{"a":{"type":"widget"}}}`
	if err := Validate(json.RawMessage(schema), json.RawMessage(`{"a":123}`)); err != nil {
		t.Fatalf("unknown keywords must be ignored: %v", err)
	}
	// Empty schema accepts any valid JSON, rejects garbage.
	if err := Validate(nil, json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatalf("nil schema rejected valid JSON: %v", err)
	}
	if err := Validate(nil, json.RawMessage(`{nope`)); err == nil {
		t.Fatal("nil schema accepted invalid JSON")
	}
	// Union types.
	if err := Validate(json.RawMessage(`{"type":["string","null"]}`), json.RawMessage(`null`)); err != nil {
		t.Fatalf("union type rejected null: %v", err)
	}
	// additionalProperties as a schema validates undeclared keys.
	apSchema := `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":{"type":"integer"}}`
	if err := Validate(json.RawMessage(apSchema), json.RawMessage(`{"a":"x","b":2}`)); err != nil {
		t.Fatalf("schema-valued additionalProperties rejected valid doc: %v", err)
	}
	if err := Validate(json.RawMessage(apSchema), json.RawMessage(`{"a":"x","b":"nope"}`)); err == nil {
		t.Fatal("schema-valued additionalProperties accepted a bad extra key")
	}
}

func TestExtractLastFenceWins(t *testing.T) {
	text := "Working notes:\n```json\n{\"draft\": true}\n```\nFinal:\n```json\n{\"draft\": false}\n```\nthanks"
	doc, err := Extract(text)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if string(doc) != `{"draft": false}` {
		t.Fatalf("doc = %s, want the LAST fence", doc)
	}
}

func TestExtractFenceLabelsAndBare(t *testing.T) {
	// Any info string works, including none.
	for _, text := range []string{
		"report\n```deliverable\n{\"ok\":1}\n```\n",
		"report\n```\n{\"ok\":1}\n```\n",
	} {
		doc, err := Extract(text)
		if err != nil || string(doc) != `{"ok":1}` {
			t.Fatalf("extract(%q) = %s, %v", text, doc, err)
		}
	}
	// Trailing bare JSON object.
	doc, err := Extract("Here is my report.\n\n{\"ok\": 2}")
	if err != nil || string(doc) != `{"ok": 2}` {
		t.Fatalf("bare extract = %s, %v", doc, err)
	}
	// A fence that isn't JSON falls through to nothing.
	if _, err := Extract("```go\nfunc main() {}\n```\nno json here"); err == nil {
		t.Fatal("expected an extraction error")
	}
}

func TestExtractPathologicalBraces(t *testing.T) {
	// A brace-heavy non-JSON tail must fail fast, not hang.
	if _, err := Extract(strings.Repeat("{", 10000)); err == nil {
		t.Fatal("expected failure on brace soup")
	}
}

func TestContractEmbedsCompactSchema(t *testing.T) {
	c := Contract(json.RawMessage("{\n  \"type\": \"object\"\n}"))
	if !strings.Contains(c, `{"type":"object"}`) {
		t.Fatalf("contract should embed the compacted schema:\n%s", c)
	}
	if !strings.Contains(c, "ABSENT") {
		t.Fatal("contract must state the absent-on-mismatch consequence")
	}
}
