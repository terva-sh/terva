//go:build terva_scripting

package tools

import (
	"encoding/json"
	"sort"
	"strings"
)

const maxScriptDeclarationBytes = 8 << 10

type schemaTypeRenderer struct {
	nodes      int
	incomplete bool
}

func scriptToolDeclaration(name string, raw json.RawMessage) string {
	var schema any
	r := &schemaTypeRenderer{}
	typ := "unknown"
	if len(raw) <= 64<<10 && json.Unmarshal(raw, &schema) == nil {
		typ = r.render(schema, 0)
	} else {
		r.incomplete = true
	}
	decl := "declare function call(name: " + tsLiteral(name) + ", args: " + typ + "): string;"
	if len(decl) > maxScriptDeclarationBytes-256 {
		decl = "declare function call(name: " + tsLiteral(name) + ", args: unknown): string;"
		r.incomplete = true
	}
	if len(decl) > maxScriptDeclarationBytes-256 {
		return "// Declaration omitted because the tool name exceeds the size limit. Read the JSON Schema."
	}
	decl += "\n// The JSON Schema defines runtime constraints."
	if r.incomplete {
		decl += "\n// Some types are incomplete. Read the JSON Schema for fields typed as unknown."
	}
	return decl
}

func tsLiteral(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (r *schemaTypeRenderer) unknown() string { r.incomplete = true; return "unknown" }

func (r *schemaTypeRenderer) render(value any, depth int) string {
	r.nodes++
	if depth > 12 || r.nodes > 512 {
		return r.unknown()
	}
	if b, ok := value.(bool); ok {
		if b {
			return "unknown"
		}
		return "never"
	}
	s, ok := value.(map[string]any)
	if !ok {
		return r.unknown()
	}
	if _, ok := s["$ref"]; ok {
		return r.unknown()
	}
	for _, key := range []string{"not", "if", "then", "else", "dependentSchemas", "dependentRequired", "unevaluatedProperties"} {
		if _, ok := s[key]; ok {
			return r.unknown()
		}
	}
	if v, ok := s["const"]; ok {
		r.markLiteralSiblings(s, "const")
		return r.literal(v)
	}
	if list, ok := s["enum"].([]any); ok {
		r.markLiteralSiblings(s, "enum")
		if len(list) == 0 || len(list) > 64 {
			return r.unknown()
		}
		parts := make([]string, 0, len(list))
		for _, v := range list {
			parts = append(parts, r.literal(v))
		}
		return "(" + strings.Join(parts, " | ") + ")"
	}
	var constraints []string
	base := make(map[string]any, len(s))
	for k, v := range s {
		base[k] = v
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := s[key].([]any); ok {
			delete(base, key)
			if len(list) == 0 || len(list) > 32 {
				return r.unknown()
			}
			parts := make([]string, 0, len(list))
			for _, v := range list {
				parts = append(parts, r.render(v, depth+1))
			}
			join := " | "
			if key == "allOf" {
				join = " & "
			}
			constraints = append(constraints, "("+strings.Join(parts, join)+")")
		}
	}
	if len(constraints) > 0 {
		for _, key := range []string{"type", "properties", "items", "additionalProperties", "required"} {
			if _, ok := base[key]; ok {
				constraints = append(constraints, r.render(base, depth+1))
				break
			}
		}
		if len(constraints) == 1 {
			return constraints[0]
		}
		return "(" + strings.Join(constraints, " & ") + ")"
	}
	if types, ok := s["type"].([]any); ok {
		if len(types) == 0 || len(types) > 16 {
			return r.unknown()
		}
		parts := make([]string, 0, len(types))
		for _, v := range types {
			copy := make(map[string]any, len(s))
			for k, x := range s {
				copy[k] = x
			}
			copy["type"] = v
			parts = append(parts, r.render(copy, depth+1))
		}
		return "(" + strings.Join(parts, " | ") + ")"
	}
	if _, ok := s["not"]; ok {
		return r.unknown()
	}
	if _, ok := s["if"]; ok {
		return r.unknown()
	}
	typeName, _ := s["type"].(string)
	switch typeName {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	case "array":
		if _, ok := s["prefixItems"]; ok {
			return "Array<" + r.unknown() + ">"
		}
		if item, ok := s["items"]; ok {
			return "Array<" + r.render(item, depth+1) + ">"
		}
		return "Array<unknown>"
	case "object":
		return r.object(s, depth)
	default:
		return r.unknown()
	}
}

func (r *schemaTypeRenderer) markLiteralSiblings(s map[string]any, selected string) {
	for _, key := range []string{"const", "enum", "type", "properties", "items", "anyOf", "oneOf", "allOf"} {
		if _, ok := s[key]; ok && key != selected {
			r.incomplete = true
		}
	}
}

func (r *schemaTypeRenderer) literal(v any) string {
	switch v.(type) {
	case nil, string, float64, bool:
		return tsLiteral(v)
	default:
		return r.unknown()
	}
}

func (r *schemaTypeRenderer) object(s map[string]any, depth int) string {
	props, _ := s["properties"].(map[string]any)
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, v := range list {
			if key, ok := v.(string); ok {
				required[key] = true
			}
		}
	}
	keys := make([]string, 0, len(props)+len(required))
	for key := range props {
		keys = append(keys, key)
	}
	for key := range required {
		if _, ok := props[key]; !ok {
			keys = append(keys, key)
		}
	}
	if len(keys) > 128 {
		return r.unknown()
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		optional := "?"
		if required[key] {
			optional = ""
		}
		parts = append(parts, tsLiteral(key)+optional+": "+r.render(props[key], depth+1)+";")
	}
	_, patterns := s["patternProperties"]
	if patterns {
		parts = append(parts, "[key: string]: "+r.unknown()+";")
	} else {
		switch extra := s["additionalProperties"].(type) {
		case bool:
			if extra {
				parts = append(parts, "[key: string]: unknown;")
			}
		case map[string]any:
			typ := r.render(extra, depth+1)
			if len(keys) > 0 {
				typ = r.unknown()
			}
			parts = append(parts, "[key: string]: "+typ+";")
		default:
			parts = append(parts, "[key: string]: unknown;")
		}
	}
	if len(parts) == 0 {
		return "Record<string, never>"
	}
	return "{ " + strings.Join(parts, " ") + " }"
}
