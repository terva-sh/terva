package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The recorded failure, and the model's FIRST structured call of the session:
// questions arrived as a string holding the JSON array. The payload was
// complete and correct. Only the quoting was wrong, and the tool threw it away.
func TestAskCoercesJSONStringQuestions(t *testing.T) {
	var a askArgs
	raw := `{"questions": "[{\"question\":\"pick one\",\"options\":[\"a\",\"b\"]}]"}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("a JSON-encoded questions array must be accepted: %v", err)
	}
	qs, err := a.questions()
	if err != nil {
		t.Fatalf("questions(): %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("got %d questions, want 1", len(qs))
	}
	if qs[0].Question != "pick one" {
		t.Errorf("question = %q, want %q", qs[0].Question, "pick one")
	}
	if got := strings.Join(qs[0].Options, ","); got != "a,b" {
		t.Errorf("options = %q, want %q", got, "a,b")
	}
}

// options suffers the same slip, at BOTH levels: the singular top-level form
// and inside an entry of the questions array.
func TestAskCoercesJSONStringOptions(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"top level", `{"question":"q","options":"[\"a\",\"b\"]"}`},
		{"nested in questions", `{"questions":[{"question":"q","options":"[\"a\",\"b\"]"}]}`},
	}
	for _, c := range cases {
		var a askArgs
		if err := json.Unmarshal([]byte(c.raw), &a); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		qs, err := a.questions()
		if err != nil {
			t.Fatalf("%s: questions(): %v", c.name, err)
		}
		if got := strings.Join(qs[0].Options, ","); got != "a,b" {
			t.Errorf("%s: options = %q, want %q", c.name, got, "a,b")
		}
	}
}

// The ordinary shape must be untouched. Coercion is a fallback, never a
// rewrite of the path every well-formed call takes.
func TestAskPlainArraysStillDecode(t *testing.T) {
	var a askArgs
	raw := `{"questions":[{"question":"q1","options":["a","b"]},{"question":"q2"}]}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("a plain array must still decode: %v", err)
	}
	qs, err := a.questions()
	if err != nil {
		t.Fatalf("questions(): %v", err)
	}
	if len(qs) != 2 || qs[0].Question != "q1" || qs[1].Question != "q2" {
		t.Fatalf("plain array decoded wrong: %+v", qs)
	}
	if got := strings.Join(qs[0].Options, ","); got != "a,b" {
		t.Errorf("options = %q, want %q", got, "a,b")
	}
}

// An empty string is an empty list. Rejecting it would fail a call that simply
// offered no options.
func TestAskEmptyStringIsAnEmptyList(t *testing.T) {
	var a askArgs
	if err := json.Unmarshal([]byte(`{"question":"q","options":""}`), &a); err != nil {
		t.Fatalf("an empty string must decode as an empty list: %v", err)
	}
	if len(a.Options) != 0 {
		t.Errorf("options = %v, want empty", a.Options)
	}
}

// The heart of the finding: when a shape genuinely cannot be used, the message
// must be in the vocabulary of the schema. It must never name a Go identifier
// the model was never shown.
func TestAskArgsErrorSpeaksSchemaNotGo(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"questions": 5}`, `the "questions" field must be an array, not a number`},
		{`{"question": 5}`, `the "question" field must be a string, not a number`},
		{`{"questions": {"question":"solo"}}`, `the "questions" field must be an array, not an object`},
		{`{"question":"q","options":"not json at all"}`, `the "options" field must be an array, not a string`},
		{`{"questions": "not json at all"}`, `the "questions" field must be an array, not a string`},
	}
	// Identifiers from the recorded message that appear in no schema.
	leaks := []string{"askArgs", "tools.askQuestion", "[]string", "Go struct field", "json:"}

	tool := &AskUserTool{}
	for _, c := range cases {
		_, err := tool.Execute(context.Background(), json.RawMessage(c.raw), nil)
		if err == nil {
			t.Errorf("%s: expected an error", c.raw)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, c.want) {
			t.Errorf("%s:\n got: %s\nwant substring: %s", c.raw, msg, c.want)
		}
		for _, leak := range leaks {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: error leaks Go internals (%q): %s", c.raw, leak, msg)
			}
		}
	}
}

// A syntax error is not a type error. It already talks about JSON, which the
// model does understand, so it passes through rather than being reworded into
// a claim about a field that may not exist.
func TestAskSyntaxErrorPassesThrough(t *testing.T) {
	tool := &AskUserTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":`), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid args") {
		t.Errorf("syntax error should still be reported as invalid args: %v", err)
	}
}

func TestJSONArrayCoercionUnit(t *testing.T) {
	t.Run("string holding an array", func(t *testing.T) {
		var a jsonArray[string]
		if err := json.Unmarshal([]byte(`"[\"x\",\"y\"]"`), &a); err != nil {
			t.Fatal(err)
		}
		if strings.Join(a, ",") != "x,y" {
			t.Errorf("got %v", a)
		}
	})
	t.Run("a string that is not an array keeps the original complaint", func(t *testing.T) {
		var a jsonArray[string]
		err := json.Unmarshal([]byte(`"plain text"`), &a)
		if err == nil {
			t.Fatal("expected an error for a string that holds no array")
		}
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			t.Fatalf("want an *UnmarshalTypeError so the caller can phrase it, got %T: %v", err, err)
		}
	})
	t.Run("a number is refused outright", func(t *testing.T) {
		var a jsonArray[string]
		if err := json.Unmarshal([]byte(`5`), &a); err == nil {
			t.Fatal("a number must not decode as an array")
		}
	})
}
