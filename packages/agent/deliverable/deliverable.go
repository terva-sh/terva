// Package deliverable implements the structured-deliverable contract for
// swarm agents (workstream B of docs/plans/jsengine-code-execution-and-
// workflows.md): a spawn can carry a JSON schema its child's report must
// match. This package owns the two halves every route shares — validating
// a candidate document against the schema, and extracting a JSON document
// from a free-text final message (the generalization of RAATI's ballot
// protocol, which stays domain-owned in packages/agent/raati).
//
// Validation covers a deliberate SUBSET of JSON Schema — type, properties,
// required, additionalProperties, items, enum, nested arbitrarily — which
// is a deliverable-shape check, not a general validator. Unknown keywords
// are ignored (the permissive default real validators take for
// annotations), so a schema written for a full validator degrades
// gracefully rather than failing here. If a contract ever needs more than
// shape (formats, bounds, refs), that is the cue to adopt a real validator
// dependency, not to grow this one keyword by keyword.
package deliverable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// FileName is the agent-state-dir file a native child's deliver_result
// tool writes and the swarm supervisor reads back at task end. Shared
// here because the writer (tools) and the reader (swarm) must never
// disagree and neither may import the other.
const FileName = "deliverable.json"

// Validate checks doc against the schema subset. A nil/empty schema
// accepts any valid JSON document. Errors carry a JSON-path location
// ("$.findings[2].severity: ...") so a model (or a human) can fix the
// exact spot.
func Validate(schema, doc json.RawMessage) error {
	if len(schema) == 0 {
		if !json.Valid(doc) {
			return fmt.Errorf("$: not valid JSON")
		}
		return nil
	}
	var s any
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("schema itself does not parse: %w", err)
	}
	var d any
	if err := json.Unmarshal(doc, &d); err != nil {
		return fmt.Errorf("$: not valid JSON: %w", err)
	}
	return validate(s, d, "$")
}

func validate(schema, doc any, path string) error {
	sm, ok := schema.(map[string]any)
	if !ok {
		// A non-object schema (true/false booleans included) constrains
		// nothing in the subset.
		return nil
	}
	if t, ok := sm["type"]; ok {
		if err := checkType(t, doc, path); err != nil {
			return err
		}
	}
	if e, ok := sm["enum"]; ok {
		if err := checkEnum(e, doc, path); err != nil {
			return err
		}
	}
	if obj, ok := doc.(map[string]any); ok {
		if req, ok := sm["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, present := obj[name]; name != "" && !present {
					return fmt.Errorf("%s: missing required property %q", path, name)
				}
			}
		}
		props, _ := sm["properties"].(map[string]any)
		for name, sub := range props {
			if v, present := obj[name]; present {
				if err := validate(sub, v, path+"."+name); err != nil {
					return err
				}
			}
		}
		switch ap := sm["additionalProperties"].(type) {
		case bool:
			if !ap {
				for name := range obj {
					if _, declared := props[name]; !declared {
						return fmt.Errorf("%s: unexpected property %q (additionalProperties: false)", path, name)
					}
				}
			}
		case map[string]any:
			for name, v := range obj {
				if _, declared := props[name]; !declared {
					if err := validate(ap, v, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
	}
	if arr, ok := doc.([]any); ok {
		if items, ok := sm["items"]; ok {
			for i, v := range arr {
				if err := validate(items, v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkType(t, doc any, path string) error {
	switch tt := t.(type) {
	case string:
		if !typeMatches(tt, doc) {
			return fmt.Errorf("%s: expected %s, got %s", path, tt, jsonTypeName(doc))
		}
	case []any:
		for _, one := range tt {
			if s, ok := one.(string); ok && typeMatches(s, doc) {
				return nil
			}
		}
		return fmt.Errorf("%s: expected one of %v, got %s", path, tt, jsonTypeName(doc))
	}
	return nil
}

func typeMatches(t string, doc any) bool {
	switch t {
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	case "array":
		_, ok := doc.([]any)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "null":
		return doc == nil
	case "number":
		_, ok := doc.(float64)
		return ok
	case "integer":
		f, ok := doc.(float64)
		return ok && f == math.Trunc(f)
	}
	// An unknown type name constrains nothing (subset permissiveness).
	return true
}

func jsonTypeName(doc any) string {
	switch v := doc.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

func checkEnum(enum, doc any, path string) error {
	vals, ok := enum.([]any)
	if !ok {
		return nil
	}
	for _, v := range vals {
		if reflect.DeepEqual(v, doc) {
			return nil
		}
	}
	return fmt.Errorf("%s: value not in enum %v", path, vals)
}

// maxBareScan bounds the trailing-bare-JSON search so a pathological
// message full of braces stays O(attempts), not O(n²).
const maxBareScan = 32

// Extract finds the deliverable document in a free-text final message:
// the LAST fenced code block whose body parses as JSON (any info string —
// ```json, ```deliverable, bare ```), else a trailing bare JSON object or
// array. This is the shape RAATI's ballot protocol proved: models reliably
// end with a fence when the contract asks for one, and the last block wins
// because earlier ones tend to be working notes.
func Extract(text string) (json.RawMessage, error) {
	if doc, ok := lastFencedJSON(text); ok {
		return doc, nil
	}
	if doc, ok := trailingBareJSON(text); ok {
		return doc, nil
	}
	return nil, fmt.Errorf("no JSON document found: expected a fenced ```json block (or a trailing bare JSON object) at the end of the message")
}

func lastFencedJSON(text string) (json.RawMessage, bool) {
	rest := text
	var last json.RawMessage
	for {
		start := strings.Index(rest, "```")
		if start < 0 {
			break
		}
		afterTick := rest[start+3:]
		nl := strings.IndexByte(afterTick, '\n')
		if nl < 0 {
			break
		}
		body := afterTick[nl+1:]
		end := strings.Index(body, "```")
		if end < 0 {
			break
		}
		candidate := strings.TrimSpace(body[:end])
		if json.Valid([]byte(candidate)) {
			last = json.RawMessage(candidate)
		}
		rest = body[end+3:]
	}
	return last, last != nil
}

func trailingBareJSON(text string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(text)
	attempts := 0
	for i := len(trimmed) - 1; i >= 0 && attempts < maxBareScan; i-- {
		if c := trimmed[i]; c != '{' && c != '[' {
			continue
		}
		attempts++
		candidate := trimmed[i:]
		if json.Valid([]byte(candidate)) {
			return json.RawMessage(candidate), true
		}
	}
	return nil, false
}

// Contract renders the prompt text that binds a child to its schema —
// identical wording for the native child addendum and the foreign-worker
// briefing, so the contract never forks between backends.
func Contract(schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString("Your report has a REQUIRED structure. End your final message with exactly one fenced ```json code block containing ONLY a JSON value that matches this schema (no prose inside the fence):\n\n```json\n")
	b.Write(compactOrRaw(schema))
	b.WriteString("\n```\n\nEverything before the fence is context for humans; the fence is what the dispatcher parses. A report whose fence is missing or does not match the schema is recorded as ABSENT.")
	return b.String()
}

func compactOrRaw(schema json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, schema); err != nil {
		return schema
	}
	return buf.Bytes()
}
