package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Two repairs for the same wound: a tool call whose arguments are RIGHT and
// whose wrapping is wrong.
//
// Encoding an array argument as a string that contains JSON is the classic
// small-model tool-call slip. In the session behind this change it happened on
// the model's very first structured call, and the tool answered with
// encoding/json's own words:
//
//	invalid args: json: cannot unmarshal string into Go struct field
//	askArgs.questions of type []tools.askQuestion
//
// That names askArgs and []tools.askQuestion — identifiers that appear in no
// schema the model was ever given. It cannot act on either. Meanwhile the
// payload it sent was complete and correct; only the quoting was wrong.
//
// So: accept it (coerceStringArrays), and when the shape is genuinely unusable,
// say so in the vocabulary of the schema rather than of the runtime
// (schemaArgsError).
//
// The repair used to live INSIDE the decode, in a slice type with its own
// UnmarshalJSON. That worked only because Go 1.25's encoding/json repaired what
// such a method returned: addErrorContext type-switched on a returned
// *UnmarshalTypeError and stamped the struct field name onto it from the
// decoder's own field stack. A method cannot know its field name; the decoder
// lent it one.
//
// Go 1.27 put the v2 implementation behind the v1 API and stopped lending.
// arshal_methods.go, on the legacy-semantics path: `return err // unlike
// marshal, never wrapped`. So the name vanished, and the tool answered a call
// carrying eight fields with "the arguments must be an array, not a number" —
// the same uselessness this file exists to end, one level down.
//
// Hence the rule everything below now follows: NOTHING in the argument path may
// intercept a value the decoder would have complained about. The decoder is the
// only thing that knows where it was standing. The repair therefore runs after
// a failure rather than inside one, and reports the original error.

// decodeArgs decodes a tool call's arguments, and retries once with the classic
// wrapping slip undone.
//
// The ordinary call pays one plain json.Unmarshal and nothing else. Only a
// failure is worth a second look, and the retry decodes into a value nobody has
// seen yet, so a half-finished decode can never leak into a call that is then
// refused. The repaired document is adopted only when the WHOLE of it decodes
// cleanly, which is also what lets coerceStringArrays stay ignorant of Go
// types: a substitution the target rejects is simply discarded.
//
// The error always describes the bytes the model sent, never our repair. That
// is the surviving half of the old "fails through the ORIGINAL error" rule. It
// held then because only that error was phrasable; it holds now on its own
// merits, because it is the message the model can act on.
func decodeArgs(raw json.RawMessage, schema string, dest any) error {
	first := json.Unmarshal(raw, dest)
	if first == nil {
		return nil
	}
	if retryCoerced(raw, schema, dest) {
		return nil
	}
	return schemaArgsError(first)
}

// retryCoerced runs the repair and reports whether it produced a document dest
// accepts. dest is written only on success.
func retryCoerced(raw json.RawMessage, schema string, dest any) bool {
	p := reflect.ValueOf(dest)
	if p.Kind() != reflect.Pointer || p.IsNil() {
		return false
	}
	coerced := coerceStringArrays(raw, schema)
	if bytes.Equal(coerced, raw) {
		return false
	}
	fresh := reflect.New(p.Type().Elem())
	if json.Unmarshal(coerced, fresh.Interface()) != nil {
		return false
	}
	p.Elem().Set(fresh.Elem())
	return true
}

// coerceStringArrays returns raw with every string that stands where the schema
// declares an array replaced by the array its text holds. Every other byte
// comes back unchanged, and an unrepairable document comes back identical.
//
// The SCHEMA decides which fields may be repaired, not the Go type. That is the
// argument schemaPropertyNames already makes next door: the schema is the one
// description the model was actually given, so a rule derived from it cannot
// drift from what the tool accepts. Reading the Go struct instead would mean
// re-spelling encoding/json's own key-to-field rule — tag, then case-insensitive
// match, then embedded fields — a second copy of a predicate the standard
// library owns.
//
// It is also the stronger guarantee. A string field whose text happens to look
// like an array, "[\"a\",\"b\"]" as the literal question to ask, is safe because
// the schema calls that field a string and the array branch is never reached.
// The old type-driven repair protected it only by the accident that no string
// field used the coercing type.
func coerceStringArrays(raw json.RawMessage, schema string) json.RawMessage {
	return coerceNode(raw, json.RawMessage(schema))
}

// schemaNode is the sliver of JSON Schema this walk reads. An unparseable node,
// or one that declares no type, returns its value untouched — the same
// fail-safe schemaPropertyNames takes when the schema will not parse.
type schemaNode struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Items      json.RawMessage            `json:"items"`
}

// coerceNode walks one value against one schema node, returning v itself when
// nothing beneath it changed. Callers rely on that identity: it is how the
// whole document reports "no repair was possible" without a second comparison.
func coerceNode(v, node json.RawMessage) json.RawMessage {
	var n schemaNode
	if len(node) == 0 || json.Unmarshal(node, &n) != nil {
		return v
	}
	switch n.Type {
	case "object":
		return coerceObject(v, n)
	case "array":
		return coerceArray(v, n)
	}
	return v
}

func coerceObject(v json.RawMessage, n schemaNode) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) != nil {
		return v
	}
	out := make(map[string]json.RawMessage, len(obj))
	changed := false
	for k, val := range obj {
		sub, ok := lookupProperty(n.Properties, k)
		if !ok {
			// A key the schema does not describe is copied verbatim. Refusing
			// it here is not this function's job; the decode that follows
			// decides what the tool accepts.
			out[k] = val
			continue
		}
		next := coerceNode(val, sub)
		if !bytes.Equal(next, val) {
			changed = true
		}
		out[k] = next
	}
	if !changed {
		return v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return v
	}
	// Re-marshaling sorts the keys and collapses a duplicate to the last of
	// its kind. Neither is observable: this document is handed straight back
	// to the decoder and discarded unless it decodes cleanly.
	return b
}

func coerceArray(v json.RawMessage, n schemaNode) json.RawMessage {
	// A string standing where an array belongs is the slip this file exists
	// for. Coercion is silent on purpose: the wrapping carries no information
	// the model needs back, and refusing it would spend a turn to teach a
	// lesson the schema already states.
	var s string
	if json.Unmarshal(v, &s) == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			// An empty string is an empty list, not a malformed one. Treating
			// it as an error would reject a call that asked for nothing.
			return json.RawMessage("null")
		}
		if !strings.HasPrefix(s, "[") || !json.Valid([]byte(s)) {
			// The string held no array, so the original complaint stands and
			// decodeArgs reports it.
			return v
		}
		// The unwrapped text re-enters the walk below, so a doubly wrapped
		// document is repaired at every level it was wrapped at.
		v = json.RawMessage(s)
	}
	if len(n.Items) == 0 {
		return v
	}
	var elems []json.RawMessage
	if json.Unmarshal(v, &elems) != nil {
		return v
	}
	changed := false
	for i, e := range elems {
		next := coerceNode(e, n.Items)
		if !bytes.Equal(next, e) {
			changed = true
		}
		elems[i] = next
	}
	if !changed {
		return v
	}
	b, err := json.Marshal(elems)
	if err != nil {
		return v
	}
	return b
}

// lookupProperty finds the schema node for a key, exactly or case-insensitively.
//
// This is not a second spelling of the decoder's field binding. The decoder
// still does every bit of that; this only decides where to LOOK for a repair,
// and a miss costs nothing — the value is copied through and the ordinary error
// stands.
func lookupProperty(props map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	if n, ok := props[key]; ok {
		return n, true
	}
	for k, n := range props {
		if strings.EqualFold(k, key) {
			return n, true
		}
	}
	return nil, false
}

// schemaArgsError restates an argument-decoding failure in the words of the
// tool's schema. encoding/json describes the Go value it could not build; a
// model only ever saw the JSON Schema, so the Go type name is noise at best
// and a false lead at worst.
//
// Anything that is not a type error passes through unchanged: a syntax error
// already talks about JSON, which is a language the model does share.
func schemaArgsError(err error) error {
	var te *json.UnmarshalTypeError
	if !errors.As(err, &te) {
		return fmt.Errorf("invalid args: %w", err)
	}
	field := schemaFieldPath(te.Field)
	if field == "" {
		return fmt.Errorf("invalid args: the arguments must be %s, not %s",
			schemaWord(te.Type), jsonWord(te.Value))
	}
	return fmt.Errorf("invalid args: the %q field must be %s, not %s",
		field, schemaWord(te.Type), jsonWord(te.Value))
}

// schemaFieldPath renders the decoder's field path in the one spelling both
// toolchains produce.
//
// A failure inside an array reads questions.0.question under Go 1.27, which
// builds the path from a JSON pointer, and questions.question under Go 1.25,
// which pushed only names onto its field stack. The index is real information,
// but only one toolchain has it, so a message built on it changes under the
// compiler and no test can pin it. Drop it and both agree.
func schemaFieldPath(field string) string {
	if !strings.Contains(field, ".") {
		return field
	}
	parts := strings.Split(field, ".")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if !isArrayIndex(p) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return field
	}
	return strings.Join(kept, ".")
}

func isArrayIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// schemaWord names a Go type the way the JSON Schema does.
func schemaWord(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "a different type"
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	}
	return "a different type"
}

// jsonWord names what actually arrived. encoding/json already reports this in
// JSON terms; only "bool" needs to become the schema's word.
func jsonWord(v string) string {
	switch v {
	case "bool":
		return "a boolean"
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "array":
		return "an array"
	case "object":
		return "an object"
	case "":
		return "something else"
	}
	return v
}
