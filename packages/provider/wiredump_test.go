package provider

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func wireReq(msgs ...Message) Request {
	return Request{
		Model:    "gpt-5.6-terra",
		System:   "you are a test",
		Messages: msgs,
		Tools:    []Tool{{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
}

func userMsg(text string) Message {
	return Message{Role: RoleUser, Content: []Content{TextBlock{Text: text}}}
}

func assistantMsg(text string) Message {
	return Message{Role: RoleAssistant, Content: []Content{TextBlock{Text: text}}}
}

// The header carries everything EXCEPT the input array, and every remaining
// line is exactly one input item. Without the split there is nothing to diff:
// the whole request is one line and "which item first differs" is back to
// reading a few hundred kilobytes by eye.
func TestDumpRequestJSONLSplitsHeaderFromItems(t *testing.T) {
	out, err := DumpRequestJSONL("openai-codex", wireReq(userMsg("one"), assistantMsg("two"), userMsg("three")))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("want a header plus one line per item, got %d lines:\n%s", len(lines), out)
	}

	var head struct {
		Dump     string                     `json:"_dump"`
		Provider string                     `json:"_provider"`
		Field    string                     `json:"_field"`
		Request  map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("header is not JSON: %v\n%s", err, lines[0])
	}
	if head.Dump != "header" || head.Provider != "openai-codex" || head.Field != "input" {
		t.Errorf("header mislabeled: %+v", head)
	}
	// The input array must be LIFTED OUT, not duplicated -- otherwise every
	// item appears twice and the header alone defeats the diff.
	if _, dup := head.Request["input"]; dup {
		t.Error("header still carries the input array; it must be lifted out")
	}
	if _, ok := head.Request["instructions"]; !ok {
		t.Error("header lost the system instructions")
	}
	if _, ok := head.Request["tools"]; !ok {
		t.Error("header lost the tool definitions")
	}
	for i, ln := range lines[1:] {
		if !json.Valid([]byte(ln)) {
			t.Errorf("item line %d is not valid JSON: %s", i, ln)
		}
	}
}

// THE property the mode exists for. A provider prompt cache matches on an exact
// byte prefix, so appending a turn must leave every earlier line untouched --
// otherwise a diff of two dumps reports churn that is the dumper's own and an
// investigation chases it. This is the assertion that would fail if anything in
// the request build ever became order- or time-dependent.
func TestDumpRequestJSONLIsAppendOnlyWhenAMessageIsAppended(t *testing.T) {
	base := []Message{userMsg("one"), assistantMsg("two")}
	before, err := DumpRequestJSONL("openai-codex", wireReq(base...))
	if err != nil {
		t.Fatal(err)
	}
	after, err := DumpRequestJSONL("openai-codex", wireReq(append(append([]Message{}, base...), userMsg("three"))...))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("appending a message rewrote earlier lines.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if len(after) <= len(before) {
		t.Errorf("appending a message did not add lines: %d -> %d bytes", len(before), len(after))
	}
}

// The dump must be a pure function of the request. Go randomizes map iteration,
// so a body that grew a map-valued field, or a builder that reached for a clock
// or a connection, would make two dumps of one request differ -- and a diff
// against a dump taken a minute earlier would be unreadable. This is the test
// the wireBody comment promises when it says the builders stay pure.
func TestDumpRequestJSONLIsDeterministic(t *testing.T) {
	req := wireReq(userMsg("one"), assistantMsg("two"))
	first, err := DumpRequestJSONL("openai-codex", req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		again, err := DumpRequestJSONL("openai-codex", req)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("dump %d differs from the first for an identical request:\n%s\n---\n%s", i, first, again)
		}
	}
}

// An unsupported provider must SAY so. Returning an empty dump would read as
// "this request carries nothing", which is the worst possible answer from a
// tool whose whole job is showing what goes on the wire.
func TestDumpRequestJSONLRefusesUnknownProvider(t *testing.T) {
	out, err := DumpRequestJSONL("anthropic", wireReq(userMsg("one")))
	if err == nil {
		t.Fatalf("want an error for an unsupported provider, got a dump:\n%s", out)
	}
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "openai-codex") {
		t.Errorf("error should name the provider asked for AND the supported ones, got: %v", err)
	}
}
