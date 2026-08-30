package modelreply

import (
	"encoding/json"
	"testing"
)

func TestLastJSONObject(t *testing.T) {
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"bare", `{"a":1}`, `{"a":1}`, true},
		{"prose either side", "hm\n{\"a\":1}\nok", `{"a":1}`, true},
		{"takes the last", `{"a":1} then {"b":2}`, `{"b":2}`, true},
		{"brace inside a string", `{"r":"literal } brace"}`, `{"r":"literal } brace"}`, true},
		{"escaped quote", `{"r":"he said \"no\""}`, `{"r":"he said \"no\""}`, true},
		{"nested", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":1}}}`, true},
		{"stray closer first", `} {"a":1}`, `{"a":1}`, true},
		{"none", "no json here", "", false},
		{"unterminated", `{"a":1`, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LastJSONObject(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The regression that motivated the extraction. raati parsed a bare ballot
// with a BACKWARD scan and no string tracking, so an unescaped brace in the
// model's own free-text rationale read as structure: the inner '}' pushed the
// depth to 2, the opening '{' only brought it back to 1, and the whole reply
// parsed as no ballot at all. The unit was then recorded absent — a vote
// silently lost to a punctuation mark in prose the model was invited to write.
//
// This is the exact shape a small local model emits, which is the only case
// the bare-object fallback exists to serve.
func TestBraceInProseDoesNotSwallowTheObject(t *testing.T) {
	const reply = `I vote thus: {"verdict":"approve","confidence":0.7,"rationale":"the cleanup in } block two is fine"}`

	got, ok := LastJSONObject(reply)
	if !ok {
		t.Fatalf("a brace inside the rationale swallowed the whole object")
	}
	var into struct {
		Verdict   string  `json:"verdict"`
		Rationale string  `json:"rationale"`
		Conf      float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(got), &into); err != nil {
		t.Fatalf("salvaged span is not valid JSON: %v (%q)", err, got)
	}
	if into.Verdict != "approve" || into.Conf != 0.7 {
		t.Fatalf("salvaged the wrong object: %+v", into)
	}
}

// A reply that narrates, quotes an earlier answer, and then answers must
// yield the FINAL object. This is what makes "last" the right rule rather
// than "first": deliberation prose that cites a previous decision must not
// shadow the current one.
func TestNarrationBeforeTheAnswerDoesNotWin(t *testing.T) {
	const reply = "Earlier I said {\"verdict\":\"reject\"} but on reflection:\n" +
		"```json\n{\"verdict\":\"approve\"}\n```"

	got, ok := LastJSONObject(reply)
	if !ok || got != `{"verdict":"approve"}` {
		t.Fatalf("got (%q,%v), want the final object", got, ok)
	}
}

// LastJSONObject narrows, it does not validate. A span that is balanced but
// not JSON must still be returned, because encoding/json is what reports the
// shape failure and it gives a better message than a hand-rolled check would.
func TestBalancedButNotJSONIsStillReturned(t *testing.T) {
	got, ok := LastJSONObject(`{verdict: approve}`)
	if !ok || got != `{verdict: approve}` {
		t.Fatalf("got (%q,%v), want the balanced span handed on for unmarshalling", got, ok)
	}
	if json.Valid([]byte(got)) {
		t.Fatalf("test premise broken: %q is meant to be invalid JSON", got)
	}
}
