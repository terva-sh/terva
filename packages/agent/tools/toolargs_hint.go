package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// A model can emit tool arguments that are valid JSON but semantically wrong:
// one key carrying what should have been several. Observed against a local
// gemma4 build, which produced
//
//	{"limit":100000,"offset=859,path":"/x.log"}
//
// where `offset=859,path` is a single key — the tool-call DSL's
// `key=value,key=value` separators written inside a JSON object.
//
// Nothing catches this. The JSON is valid, so FinalizeToolArguments passes it
// through untouched; json.Unmarshal into a typed args struct SUCCEEDS, binding
// `limit` and silently discarding the key it does not know, which leaves the
// required field at its zero value. The tool then reports only that the field
// is missing — true, but useless: the model DID supply a path, attached to a
// key nobody reads. Given no better information it repeats the same mistake,
// as it did three times in the session that surfaced this.
//
// Naming the unrecognised keys turns an opaque failure into a self-correcting
// one. This is a diagnostic, not validation: unknown keys are still ignored,
// and a call that otherwise works is never refused for carrying one.

// schemaPropertyNames lists the argument names a tool declares in its JSON
// schema. The schema is the single source of truth the model was given, so
// deriving the known set from it cannot drift from what the tool accepts —
// a hand-maintained list would, and every entry it lost would become a false
// "unexpected key" on a perfectly good call.
//
// A schema that cannot be parsed yields nothing, which disables the hint
// rather than risking a wrong one.
func schemaPropertyNames(schema string) []string {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return nil
	}
	names := make([]string, 0, len(doc.Properties))
	for k := range doc.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// unexpectedArgKeys reports the keys present in raw that the tool does not
// declare, sorted for a stable message.
//
// Anything that is not a JSON object yields nothing: a malformed or non-object
// payload fails earlier and more clearly on its own, and guessing at it here
// would only add noise to that error.
func unexpectedArgKeys(raw json.RawMessage, known ...string) []string {
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil
	}
	if len(got) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(known))
	for _, k := range known {
		set[k] = struct{}{}
	}
	var out []string
	for k := range got {
		if _, ok := set[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// unexpectedArgKeyHint renders the keys as a suffix a caller appends to its own
// error, or "" when every key is recognised. The leading space belongs to the
// hint so the common case stays exactly as it was, with no trailing whitespace
// on messages that carry no hint.
func unexpectedArgKeyHint(raw json.RawMessage, known ...string) string {
	keys := unexpectedArgKeys(raw, known...)
	if len(keys) == 0 {
		return ""
	}
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = fmt.Sprintf("%q", k)
	}
	noun := "key"
	if len(keys) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf(" (unrecognised argument %s %s — check the separators in the arguments)",
		noun, strings.Join(quoted, ", "))
}

// argHint is the form the tools use: the known names come from the tool's own
// schema, so a call site cannot fall out of step with what it accepts.
func argHint(raw json.RawMessage, schema string) string {
	return unexpectedArgKeyHint(raw, schemaPropertyNames(schema)...)
}
