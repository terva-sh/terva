package provider

import (
	"bytes"
	"context"
	"encoding/base64"
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

// An image_generation_call output item is decoded into an assistant
// ImageBlock (carrying the ig_ id for later editing), and — being
// already-complete output, not a function call terva must run — the turn
// stops StopEnd, not StopToolUse.
func TestCodexParsesImageGenerationCall(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	frames := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ig_test1","type":"image_generation_call","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ig_test1","type":"image_generation_call","status":"completed","output_format":"png","result":"` + b64 + `"}}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(frames))}
	out := make(chan Event, 32)
	go c.runStream(context.Background(), resp, Request{Model: "gpt-5.5"}, out)

	var done *EventDone
	for ev := range out {
		if d, ok := ev.(EventDone); ok {
			dd := d
			done = &dd
		}
	}
	if done == nil {
		t.Fatal("no EventDone")
	}
	if done.Stop != StopEnd {
		t.Fatalf("stop = %v, want StopEnd (an image call is not a tool call)", done.Stop)
	}
	var img *ImageBlock
	for _, ct := range done.Message.Content {
		if ib, ok := ct.(ImageBlock); ok {
			ibb := ib
			img = &ibb
		}
	}
	if img == nil {
		t.Fatalf("no ImageBlock in assistant message: %+v", done.Message.Content)
	}
	if img.ID != "ig_test1" {
		t.Errorf("ImageBlock.ID = %q, want ig_test1", img.ID)
	}
	if img.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", img.MimeType)
	}
	if !bytes.Equal(img.Data, pngBytes) {
		t.Errorf("Data mismatch: got %d bytes, want %d", len(img.Data), len(pngBytes))
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

// With ImageOutput set, the codex request offers the built-in
// image_generation tool alongside any function tools, with tool_choice
// auto so the model may draw on its own. Without it, no image tool.
func TestCodexOffersImageGenerationTool(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model:       "gpt-5.5",
		Tools:       []Tool{{Name: "read", Description: "read a file", Schema: []byte(`{"type":"object"}`)}},
		ImageOutput: &ImageOutputConfig{Size: "1024x1024", Quality: "low"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var img *codexImageTool
	sawFn := false
	for _, tl := range wire.Tools {
		switch v := tl.(type) {
		case codexImageTool:
			vv := v
			img = &vv
		case codexTool:
			if v.Name == "read" {
				sawFn = true
			}
		}
	}
	if img == nil {
		t.Fatalf("no image_generation tool in %+v", wire.Tools)
	}
	if img.Type != "image_generation" || img.Size != "1024x1024" || img.Quality != "low" {
		t.Fatalf("image tool = %+v", img)
	}
	if !sawFn {
		t.Fatalf("function tool dropped when image output enabled")
	}
	if wire.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %q, want auto", wire.ToolChoice)
	}

	plain, err := c.buildRequest(Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range plain.Tools {
		if _, ok := tl.(codexImageTool); ok {
			t.Fatalf("image tool offered without ImageOutput")
		}
	}
}

// Editing replay: the most-recent EditHistory assistant images are
// replayed as image_generation_call input items with their bytes; older
// ones are not; and none are replayed when the image tool isn't offered.
func TestCodexEditHistoryReplayCap(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	data1 := []byte("img-one")
	data2 := []byte("img-two")
	msgs := []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "draw a circle"}}},
		{Role: RoleAssistant, Content: []Content{ImageBlock{MimeType: "image/png", Data: data1, ID: "ig_1"}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "draw a square"}}},
		{Role: RoleAssistant, Content: []Content{ImageBlock{MimeType: "image/png", Data: data2, ID: "ig_2"}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "make the last one blue"}}},
	}
	replayed := func(wire *codexRequest) map[string]string {
		got := map[string]string{}
		for _, item := range wire.Input {
			if g, ok := item.(codexImageGenCall); ok {
				got[g.ID] = g.Result
			}
		}
		return got
	}

	wire, err := c.buildRequest(Request{Model: "gpt-5.5", Messages: msgs, ImageOutput: &ImageOutputConfig{EditHistory: 1}})
	if err != nil {
		t.Fatal(err)
	}
	got := replayed(wire)
	if len(got) != 1 {
		t.Fatalf("EditHistory=1 replayed %v, want only ig_2", got)
	}
	if _, ok := got["ig_1"]; ok {
		t.Fatalf("ig_1 must not replay at EditHistory=1")
	}
	if want := base64.StdEncoding.EncodeToString(data2); got["ig_2"] != want {
		t.Fatalf("ig_2 result did not carry the bytes")
	}

	wire2, _ := c.buildRequest(Request{Model: "gpt-5.5", Messages: msgs, ImageOutput: &ImageOutputConfig{EditHistory: 2}})
	if got := replayed(wire2); len(got) != 2 {
		t.Fatalf("EditHistory=2 replayed %d, want 2", len(got))
	}

	wire0, _ := c.buildRequest(Request{Model: "gpt-5.5", Messages: msgs})
	if got := replayed(wire0); len(got) != 0 {
		t.Fatalf("no ImageOutput replayed %d, want 0", len(got))
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
