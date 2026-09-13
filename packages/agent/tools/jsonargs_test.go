package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The recorded failure, and the model's FIRST structured call of the session:
// questions arrived as a string holding the JSON array. The payload was
// complete and correct. Only the quoting was wrong, and the tool threw it away.
func TestAskCoercesJSONStringQuestions(t *testing.T) {
	var a askArgs
	raw := `{"questions": "[{\"question\":\"pick one\",\"options\":[\"a\",\"b\"]}]"}`
	if err := decodeArgs(json.RawMessage(raw), askSchema, &a); err != nil {
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
		if err := decodeArgs(json.RawMessage(c.raw), askSchema, &a); err != nil {
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
	if err := decodeArgs(json.RawMessage(raw), askSchema, &a); err != nil {
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
	if err := decodeArgs(json.RawMessage(`{"question":"q","options":""}`), askSchema, &a); err != nil {
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

// The walk, on bytes. It may only touch a value the schema calls an array, and
// an unrepairable document has to come back byte-identical — decodeArgs reads
// that identity as "no repair was possible" and reports the original error.
func TestCoerceStringArraysUnit(t *testing.T) {
	cases := []struct {
		name, raw string
		same      bool // comes back byte-identical
		want      func(*testing.T, askArgs)
	}{
		{
			name: "a string holding an array is unwrapped",
			raw:  `{"question":"q","options":"[\"x\",\"y\"]"}`,
			want: func(t *testing.T, a askArgs) {
				if got := strings.Join(a.Options, ","); got != "x,y" {
					t.Errorf("options = %q, want x,y", got)
				}
			},
		},
		{
			name: "a string holding no array is left alone",
			raw:  `{"question":"q","options":"plain text"}`,
			same: true,
		},
		{
			name: "a number where an array belongs is left alone",
			raw:  `{"questions":5}`,
			same: true,
		},
		{
			name: "an empty string becomes an empty list",
			raw:  `{"question":"q","options":""}`,
			want: func(t *testing.T, a askArgs) {
				if len(a.Options) != 0 {
					t.Errorf("options = %v, want empty", a.Options)
				}
			},
		},
		{
			name: "a doubly wrapped document is unwrapped at both levels",
			raw:  `{"questions":"[{\"question\":\"q\",\"options\":\"[\\\"a\\\"]\"}]"}`,
			want: func(t *testing.T, a askArgs) {
				if len(a.Questions) != 1 {
					t.Fatalf("got %d questions, want 1", len(a.Questions))
				}
				if got := strings.Join(a.Questions[0].Options, ","); got != "a" {
					t.Errorf("nested options = %q, want a", got)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coerceStringArrays(json.RawMessage(c.raw), askSchema)
			if c.same {
				if string(got) != c.raw {
					t.Fatalf("document was rewritten:\n got: %s\nwant: %s", got, c.raw)
				}
				return
			}
			if string(got) == c.raw {
				t.Fatalf("nothing was repaired in %s", c.raw)
			}
			var a askArgs
			if err := json.Unmarshal(got, &a); err != nil {
				t.Fatalf("the repaired document does not decode: %v", err)
			}
			c.want(t, a)
		})
	}
}

// 🪤 The guarantee the schema-driven walk makes and the old type-driven one got
// only by accident: a STRING field whose text looks like an array is text. The
// old repair was safe here because no string field happened to use the coercing
// type — a property of the struct, not a decision anyone made.
func TestAskLeavesAStringFieldThatLooksLikeAnArray(t *testing.T) {
	var a askArgs
	raw := `{"question":"[\"a\",\"b\"]"}`
	if err := decodeArgs(json.RawMessage(raw), askSchema, &a); err != nil {
		t.Fatalf("decodeArgs: %v", err)
	}
	if a.Question != `["a","b"]` {
		t.Errorf("question = %q, want the literal text it was sent as", a.Question)
	}
}

// A failure inside the questions array names its path, in the one spelling both
// toolchains produce. Go 1.27 reports questions.0.question and Go 1.25 reports
// questions.question; schemaFieldPath drops the index so the message does not
// change under the compiler.
//
// This is also the test that fails if a custom UnmarshalJSON is ever
// reintroduced anywhere under askArgs: the field path is exactly what such a
// method destroys.
func TestAskNestedFieldErrorNamesItsPath(t *testing.T) {
	tool := &AskUserTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"questions":[{"question":5}]}`), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `the "questions.question" field must be a string, not a number`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got: %s\nwant substring: %s", err, want)
	}
}

// The error describes the bytes the MODEL sent, never the repair we attempted.
// options holds a JSON array of numbers, so the repair unwraps it and the retry
// then fails on element 0 — and reporting that would tell the model about a
// document it never wrote.
func TestAskReportsTheBytesTheModelSent(t *testing.T) {
	tool := &AskUserTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q","options":"[1,2]"}`), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `the "options" field must be an array, not a string`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got: %s\nwant substring: %s", err, want)
	}
	if strings.Contains(err.Error(), "options.0") {
		t.Errorf("the error describes our repaired document, not the model's: %s", err)
	}
}

// The second caller of decodeArgs gets the same vocabulary. Nothing else in
// ticket_init reads the arguments, so a regression here would be silent.
func TestTicketInitArgsSpeakSchemaNotGo(t *testing.T) {
	tool := &TicketInitTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"instructions": 5}`), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `the "instructions" field must be a boolean, not a number`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got: %s\nwant substring: %s", err, want)
	}
}
