package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// A thought signature cannot be reconstructed, so a tool call that reaches a
// Gemini 3 model without one can never be replayed as a functionCall part —
// the API answers 400 "Function call is missing a thought_signature in
// functionCall parts" and the turn dies.
//
// The signature round-trip covers the case where terva HAS the signature. It
// does nothing for the case where there was never one to keep, and that case
// is ordinary: Agent.SetModel swaps the model id inside a live session and
// keeps the transcript, which is precisely what it is for. So gemini-2.5-pro →
// gemini-3.x after any tool loop replayed calls with no signature and hit the
// same 400 the round-trip exists to prevent. History built on another provider,
// or handed back by an SDK embedder, arrives the same way.
//
// These tests pin the flattening that fixes it, and — as much as they pin the
// fix — that it stays OFF the ordinary path.

// swapHistory is a transcript carrying one completed tool loop whose call has
// no signature: what a session looks like the instant after a model swap.
func swapHistory() []Message {
	return []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "find the files"}}},
		{Role: RoleAssistant, Content: []Content{ToolCallBlock{
			ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`),
		}}},
		{Role: RoleTool, Content: []Content{ToolResultBlock{
			CallID: "call_1", Content: []Content{TextBlock{Text: "a.go"}},
		}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "now read it"}}},
	}
}

// allParts flattens the request back to a part list, which is what the API
// actually validates — a call hiding in any content is still a 400.
func allParts(wire *gemRequest) []gemPart {
	var out []gemPart
	for _, c := range wire.Contents {
		out = append(out, c.Parts...)
	}
	return out
}

func partsText(parts []gemPart) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// The call itself: nothing that would 400 may reach the wire, and what replaces
// it has to still say a tool ran. Dropping it silently would leave the model
// looking at a turn where it said nothing, having in fact globbed the tree.
func TestAnUnsignedCallIsFlattenedRatherThanReplayed(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-3-pro-preview", Messages: swapHistory()})
	if err != nil {
		t.Fatal(err)
	}
	parts := allParts(wire)
	for _, p := range parts {
		if p.FunctionCall != nil {
			t.Fatalf("an unsigned functionCall reached the wire; this request 400s: %+v", p.FunctionCall)
		}
	}
	text := partsText(parts)
	if !strings.Contains(text, geminiFlattenedCallTag) {
		t.Fatalf("the call vanished instead of being narrated: %q", text)
	}
	// The name and arguments are the whole point of keeping it — a bare
	// "a tool ran" tells the model nothing it can use.
	if !strings.Contains(text, "glob") || !strings.Contains(text, `"pattern":"*.go"`) {
		t.Fatalf("the narration dropped the call's name or arguments: %q", text)
	}
}

// The result has to go with it. A functionResponse whose functionCall is gone
// is an orphan, which the API rejects on its own account — so flattening one
// half would trade a 400 for a different 400.
func TestTheResultAnsweringAnUnsignedCallIsFlattenedToo(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-3-pro-preview", Messages: swapHistory()})
	if err != nil {
		t.Fatal(err)
	}
	parts := allParts(wire)
	for _, p := range parts {
		if p.FunctionResponse != nil {
			t.Fatalf("an orphaned functionResponse reached the wire: %+v", p.FunctionResponse)
		}
	}
	text := partsText(parts)
	if !strings.Contains(text, geminiFlattenedResultTag) || !strings.Contains(text, "a.go") {
		t.Fatalf("the tool's output was lost rather than narrated: %q", text)
	}
}

// An error result keeps its own tag. "The tool answered" and "the tool failed"
// are different facts, and collapsing them would have the model plan against an
// error it read as output.
func TestAFlattenedErrorResultSaysItFailed(t *testing.T) {
	msgs := swapHistory()
	msgs[2] = Message{Role: RoleTool, Content: []Content{ToolResultBlock{
		CallID: "call_1", IsError: true, Content: []Content{TextBlock{Text: "no such file"}},
	}}}
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-3-pro-preview", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	text := partsText(allParts(wire))
	if !strings.Contains(text, geminiFlattenedErrorTag) {
		t.Fatalf("a failed tool call was narrated as a successful one: %q", text)
	}
}

// OFF the ordinary path. A Gemini 3 session's own calls carry signatures, and
// flattening those would throw away the structured form for nothing — the
// model would lose its own tool loop mid-session.
func TestASignedCallIsLeftAloneOnTheSameModel(t *testing.T) {
	msgs := swapHistory()
	msgs[1] = Message{Role: RoleAssistant, Content: []Content{ToolCallBlock{
		ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`),
		Signature: "SIG-OPAQUE-1",
	}}}
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-3-pro-preview", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var calls, responses int
	for _, p := range allParts(wire) {
		if p.FunctionCall != nil {
			calls++
			if p.ThoughtSignature != "SIG-OPAQUE-1" {
				t.Fatalf("thoughtSignature = %q, want it replayed", p.ThoughtSignature)
			}
		}
		if p.FunctionResponse != nil {
			responses++
		}
	}
	if calls != 1 || responses != 1 {
		t.Fatalf("signed loop = %d calls / %d responses, want 1/1 — flattening reached a call it should not have", calls, responses)
	}
}

// And off the older generation entirely. 2.5 never issues a signature and never
// asks for one, so flattening there would degrade every one of its tool loops
// to prose in exchange for nothing.
func TestAnUnsignedCallSurvivesOnAModelThatDoesNotRequireASignature(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-2.5-pro", Messages: swapHistory()})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, p := range allParts(wire) {
		if p.FunctionCall != nil {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("functionCall parts = %d, want the 2.5 loop untouched", calls)
	}
}

// Per call, not per turn. One assistant message can hold several calls, and a
// blanket decision on the message would either strand the signed one or replay
// the unsigned one.
func TestAMixedTurnFlattensOnlyTheUnsignedCall(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "look around"}}},
		{Role: RoleAssistant, Content: []Content{
			ToolCallBlock{ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`)},
			ToolCallBlock{ID: "call_2", Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`), Signature: "SIG-2"},
		}},
		{Role: RoleTool, Content: []Content{
			ToolResultBlock{CallID: "call_1", Content: []Content{TextBlock{Text: "a.go"}}},
			ToolResultBlock{CallID: "call_2", Content: []Content{TextBlock{Text: "package main"}}},
		}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "and now?"}}},
	}
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{Model: "gemini-3-pro-preview", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	parts := allParts(wire)
	var names []string
	var responses int
	for _, p := range parts {
		if p.FunctionCall != nil {
			names = append(names, p.FunctionCall.Name)
		}
		if p.FunctionResponse != nil {
			responses++
		}
	}
	if len(names) != 1 || names[0] != "read" {
		t.Fatalf("surviving calls = %v, want only the signed one (read)", names)
	}
	if responses != 1 {
		t.Fatalf("surviving functionResponses = %d, want only the signed call's", responses)
	}
	// The unsigned half is still in the conversation, as text.
	text := partsText(parts)
	if !strings.Contains(text, geminiFlattenedCallTag) || !strings.Contains(text, "glob") {
		t.Fatalf("the unsigned call was dropped rather than narrated: %q", text)
	}
}

// The rolling aliases resolve to Gemini 3, so they require a signature too.
// This is the case a `strings.Contains(id, "gemini-3")` test would miss, and
// missing it means the default model 400s on its second tool call.
func TestTheRollingAliasesRequireASignature(t *testing.T) {
	for _, id := range []string{"gemini-flash-latest", "gemini-flash-lite-latest"} {
		if !geminiRequiresThoughtSignature(id) {
			t.Errorf("%s: reported as not requiring a signature; it resolves to a Gemini 3 model", id)
		}
	}
	for _, id := range []string{"gemini-2.5-pro", "gemini-2.5-flash-latest"} {
		if geminiRequiresThoughtSignature(id) {
			t.Errorf("%s: reported as requiring a signature; flattening would degrade its tool loops", id)
		}
	}
}
