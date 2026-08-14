package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Gemini 3 attaches a `thoughtSignature` to the part carrying a functionCall
// and then REQUIRES it back on the next request. Dropping it fails the whole
// turn with:
//
//	http 400: Function call is missing a thought_signature in functionCall
//	parts … function call `default_api:glob`, position 2
//
// which is not a degradation but a hard stop: the second tool call of a
// session is where it lands, so the agent loop dies on its first follow-up.
// These tests pin both halves of the round-trip — capture off the stream, and
// replay into the request.

// The streamed signature must land on the assembled ToolCallBlock. Without it
// nothing downstream can replay what it never received.
func TestGeminiStreamCapturesThoughtSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"glob","args":{"pattern":"*.go"}},"thoughtSignature":"SIG-OPAQUE-1"}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-3-pro-preview"})
	if err != nil {
		t.Fatal(err)
	}
	var done EventDone
	for ev := range evs {
		if e, ok := ev.(EventDone); ok {
			done = e
		}
	}
	if len(done.Message.Content) != 1 {
		t.Fatalf("content = %d blocks, want 1: %+v", len(done.Message.Content), done.Message.Content)
	}
	tc, ok := done.Message.Content[0].(ToolCallBlock)
	if !ok {
		t.Fatalf("block is %T, want ToolCallBlock", done.Message.Content[0])
	}
	if tc.Signature != "SIG-OPAQUE-1" {
		t.Fatalf("Signature = %q, want the streamed thoughtSignature; the next request will 400 without it", tc.Signature)
	}
}

// A tool call replayed in history must carry its signature back on the same
// part as the functionCall — that placement is what the API validates.
func TestGeminiBuildRequestReplaysThoughtSignature(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model: "gemini-3-pro-preview",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "find the files"}}},
			{Role: RoleAssistant, Content: []Content{ToolCallBlock{
				ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`),
				Signature: "SIG-OPAQUE-1",
			}}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{
				CallID: "call_1", Content: []Content{TextBlock{Text: "a.go"}},
			}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "now read it"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found *gemPart
	for i := range wire.Contents {
		for j := range wire.Contents[i].Parts {
			if wire.Contents[i].Parts[j].FunctionCall != nil {
				found = &wire.Contents[i].Parts[j]
			}
		}
	}
	if found == nil {
		t.Fatalf("no functionCall part in the request: %+v", wire.Contents)
	}
	if found.ThoughtSignature != "SIG-OPAQUE-1" {
		t.Fatalf("thoughtSignature = %q, want it replayed on the functionCall part", found.ThoughtSignature)
	}
	// And it must serialize under the exact key the API reads.
	raw, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"thoughtSignature":"SIG-OPAQUE-1"`) {
		t.Fatalf("wire JSON lacks the thoughtSignature key: %s", raw)
	}
}

// Models that never issue a signature (2.5 and older) must not gain an empty
// key on the wire; an unexpected field is its own class of 400.
func TestGeminiBuildRequestOmitsEmptyThoughtSignature(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model: "gemini-2.5-pro",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{ToolCallBlock{
				ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`),
			}}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{
				CallID: "call_1", Content: []Content{TextBlock{Text: "ok"}},
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "thoughtSignature") {
		t.Fatalf("empty signature leaked onto the wire: %s", raw)
	}
}
