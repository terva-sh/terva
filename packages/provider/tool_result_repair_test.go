package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func repairCall(id string) Message {
	return Message{Role: RoleAssistant, Content: []Content{ToolCallBlock{ID: id, Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)}}}
}

func repairResult(id, text string) Message {
	return Message{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: id, Content: []Content{TextBlock{Text: text}}}}}
}

func TestRepairMatchesPrecedingCallOccurrences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []Message
	}{
		{"ordinary", []Message{repairCall("a"), repairResult("a", "first")}, []Message{repairCall("a"), repairResult("a", "first")}},
		{"duplicate", []Message{repairCall("a"), repairResult("a", "first"), repairResult("a", "duplicate")}, []Message{repairCall("a"), repairResult("a", "first")}},
		{"reused", []Message{repairCall("a"), repairResult("a", "first"), repairCall("a"), repairResult("a", "second")}, []Message{repairCall("a"), repairResult("a", "first"), repairCall("a"), repairResult("a", "second")}},
		{"future call", []Message{repairResult("a", "premature"), repairCall("a"), repairResult("a", "first")}, []Message{repairCall("a"), repairResult("a", "first")}},
		{"adjacent batch", []Message{repairCall("a"), repairCall("b"), repairResult("b", "second"), repairResult("a", "first"), repairResult("b", "duplicate")}, []Message{repairCall("a"), repairCall("b"), repairResult("b", "second"), repairResult("a", "first")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.in)
			got := RepairOrphanedToolResults(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			after, _ := json.Marshal(tc.in)
			if string(before) != string(after) {
				t.Fatal("repair mutated input")
			}
		})
	}
}

func TestProviderRequestsRepairDuplicateToolResults(t *testing.T) {
	req := Request{Model: "gpt-5.5", Messages: []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "start"}}},
		repairCall("same"), repairResult("same", "kept-first"), repairResult("same", "discard-duplicate"),
		repairCall("same"), repairResult("same", "kept-second"),
	}}
	before, _ := json.Marshal(req)
	builders := map[string]func() (any, error){
		"openai":    func() (any, error) { return (&openaiClient{}).buildRequest(req) },
		"codex":     func() (any, error) { return (&codexClient{}).buildRequest(req) },
		"anthropic": func() (any, error) { return (&anthropicClient{}).buildRequest(req) },
		"gemini":    func() (any, error) { body, _, err := (&geminiClient{}).buildRequest(req); return body, err },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			body, err := build()
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"kept-first", "kept-second"} {
				if strings.Count(string(wire), text) != 1 {
					t.Fatalf("expected one %q in %s", text, wire)
				}
			}
			if strings.Contains(string(wire), "discard-duplicate") {
				t.Fatalf("duplicate in %s", wire)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("builder mutated input request")
			}
		})
	}
}
