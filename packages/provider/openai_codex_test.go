package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCodexNestedStreamError(t *testing.T) {
	// The GPT-5.6 preview backend reports errors nested under "error".
	// The handler must surface that message, not a blank string.
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"error\",\"error\":{\"code\":\"model_not_available\",\"message\":\"limited preview\"}}\n\n")),
	}
	out := make(chan Event, 16)
	go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

	var got error
	for ev := range out {
		if done, ok := ev.(EventDone); ok {
			got = done.Err
		}
	}
	if got == nil || got.Error() != "openai-codex: limited preview" {
		t.Fatalf("error = %v", got)
	}
}

func TestCodexResponseFailedTransience(t *testing.T) {
	const retryMessage = "An error occurred while processing your request. You can retry your request, or contact support if the error persists."
	cases := []struct {
		name      string
		payload   string
		transient bool
	}{
		{
			name:      "server error code",
			payload:   `{"type":"response.failed","response":{"error":{"code":"server_error","message":"failed"}}}`,
			transient: true,
		},
		{
			name:      "canonical retry advice without code",
			payload:   `{"type":"response.failed","response":{"error":{"message":"` + retryMessage + `"}}}`,
			transient: true,
		},
		{
			name:      "invalid request remains permanent",
			payload:   `{"type":"response.failed","response":{"error":{"code":"invalid_request_error","message":"bad input"}}}`,
			transient: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewOpenAICodex("token", "acct", "").(*codexClient)
			resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + tc.payload + "\n\n"))}
			out := make(chan Event, 16)
			go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

			var got error
			for ev := range out {
				if done, ok := ev.(EventDone); ok {
					got = done.Err
				}
			}
			pe, ok := got.(*ProviderError)
			if !ok {
				t.Fatalf("error = %T %v, want *ProviderError", got, got)
			}
			if pe.Transient != tc.transient {
				t.Fatalf("Transient = %v, want %v (error %v)", pe.Transient, tc.transient, pe)
			}
		})
	}
}

// GPT-5.6 supports a native "max" reasoning effort above xhigh; the
// codex request builder must send it verbatim for those models.
func TestGPT56UsesNativeMaxReasoningEffort(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{Model: "gpt-5.6-sol", Reasoning: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Reasoning == nil || wire.Reasoning.Effort != "max" {
		t.Fatalf("reasoning = %+v, want effort=max", wire.Reasoning)
	}
}

// An image-only tool result must not serialize to an empty
// function_call_output (the Responses API may reject it) and a
// following user-message image must serialize as input_image so the
// model actually receives the bytes.
func TestCodexImageToolResultMirror(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)

	wire, err := c.buildRequest(Request{
		Model: "gpt-5.5",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "look at this"}}},
			{Role: RoleAssistant, Content: []Content{
				ToolCallBlock{ID: "call_1", Name: "read", Arguments: []byte(`{"path":"x.png"}`)},
			}},
			{Role: RoleTool, Content: []Content{
				ToolResultBlock{CallID: "call_1", Content: []Content{
					ImageBlock{MimeType: "image/png", Data: []byte("png-bytes")},
				}},
			}},
			// The agent loop appends this mirror after an image tool result.
			{Role: RoleUser, Content: []Content{
				TextBlock{Text: "Tool output included the following image content:"},
				ImageBlock{MimeType: "image/png", Data: []byte("png-bytes")},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawFnOutput, sawInputImage bool
	for _, item := range wire.Input {
		switch v := item.(type) {
		case codexFunctionCallOutput:
			sawFnOutput = true
			if strings.TrimSpace(v.Output) == "" {
				t.Fatalf("image-only tool result produced empty function_call_output")
			}
			if !strings.Contains(strings.ToLower(v.Output), "image") {
				t.Fatalf("placeholder should mention image, got %q", v.Output)
			}
		case codexInputMessage:
			for _, ct := range v.Content {
				if img, ok := ct.(codexInputImage); ok && img.Type == "input_image" {
					sawInputImage = true
				}
			}
		}
	}
	if !sawFnOutput {
		t.Fatalf("no function_call_output emitted")
	}
	if !sawInputImage {
		t.Fatalf("mirrored user image was not serialized as input_image")
	}
}
