package tools

import (
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
// So: accept it (jsonArray), and when the shape is genuinely unusable, say so
// in the vocabulary of the schema rather than of the runtime
// (schemaArgsError).

// jsonArray is a slice that also accepts its own JSON text inside a string.
// ["a","b"] and "[\"a\",\"b\"]" both decode to the same two elements.
//
// Coercion is silent on purpose. The wrapping slip carries no information the
// model needs back — refusing it would spend a turn to teach a lesson the
// schema already states. A shape that is genuinely ambiguous still fails, and
// fails through the ORIGINAL error, so the caller can phrase it.
type jsonArray[T any] []T

func (a *jsonArray[T]) UnmarshalJSON(b []byte) error {
	// The ordinary path: a real JSON array.
	var direct []T
	directErr := json.Unmarshal(b, &direct)
	if directErr == nil {
		*a = direct
		return nil
	}
	// Not an array. It is only worth a second look when it is a string,
	// because that is the slip this type exists for.
	var s string
	if json.Unmarshal(b, &s) != nil {
		return directErr
	}
	s = strings.TrimSpace(s)
	if s == "" {
		// An empty string is an empty list, not a malformed one. Treating it
		// as an error would reject a call that asked for nothing.
		*a = nil
		return nil
	}
	var inner []T
	if json.Unmarshal([]byte(s), &inner) != nil {
		// The string did not hold an array either, so the original complaint
		// stands and the caller reports it.
		return directErr
	}
	*a = inner
	return nil
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
	if te.Field == "" {
		return fmt.Errorf("invalid args: the arguments must be %s, not %s",
			schemaWord(te.Type), jsonWord(te.Value))
	}
	return fmt.Errorf("invalid args: the %q field must be %s, not %s",
		te.Field, schemaWord(te.Type), jsonWord(te.Value))
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
