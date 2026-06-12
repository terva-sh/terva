package provider

import (
	"context"
	"testing"
)

// The agent loop decides whether to mirror tool-result images into a
// following user message by asking the client for MirrorsToolImages().
// Every OpenAI chat-completions provider (and the Responses/codex route)
// must opt in — their wire formats can't carry images in a tool result.
// MirrorsToolImages describes the wire format only: deepseek now opts
// in like every other chat-completions client, and whether a given
// MODEL can see images is the per-model image-input capability the
// agent loop checks separately (docs/plans/model-capabilities.md).
// Providers that carry tool images natively (Anthropic, Gemini) must
// NOT implement it.
func TestMirrorsToolImagesCapability(t *testing.T) {
	mirrors := func(c Client) (implements, value bool) {
		m, ok := c.(interface{ MirrorsToolImages() bool })
		if !ok {
			return false, false
		}
		return true, m.MirrorsToolImages()
	}

	cases := []struct {
		name       string
		client     Client
		wantImpl   bool
		wantMirror bool
	}{
		{"openai", NewOpenAI("k", ""), true, true},
		{"groq", NewGroq("k", ""), true, true},
		{"openai-codex", NewOpenAICodex("k", "", ""), true, true},
		{"deepseek", NewDeepSeek("k", ""), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			impl, val := mirrors(tc.client)
			if impl != tc.wantImpl {
				t.Fatalf("implements MirrorsToolImages = %v, want %v", impl, tc.wantImpl)
			}
			if val != tc.wantMirror {
				t.Errorf("MirrorsToolImages() = %v, want %v", val, tc.wantMirror)
			}
		})
	}
}

// The capability MUST survive through the wrappers the build layer
// actually ships: openai-codex is always wrapped in RefreshingClient,
// and openai-responses / google-vertex in renamedClient. A direct type
// assertion on the outermost client returns false for these, so the
// agent loop uses ClientMirrorsToolImages, which unwraps recursively.
// This is the regression test for the inert-mirroring bug (deep-review
// Part B #7): the raw constructors opt in, but the wrapped clients that
// ship silently didn't.
func TestClientMirrorsToolImagesThroughWrappers(t *testing.T) {
	noopRefresh := func(context.Context) (string, error) { return "", nil }
	noopFactory := func(token string) Client { return NewOpenAICodex(token, "", "") }

	cases := []struct {
		name string
		c    Client
		want bool
	}{
		// Raw clients (baseline: helper agrees with the direct assertion).
		{"raw-openai", NewOpenAI("k", ""), true},
		{"raw-codex", NewOpenAICodex("k", "", ""), true},
		// deepseek speaks the same chat-completions wire; its former
		// false here was a model capability in client clothing.
		{"raw-deepseek", NewDeepSeek("k", ""), true},
		// Anthropic carries tool images natively: must NOT opt in.
		{"raw-anthropic", NewAnthropic("k", ""), false},

		// Codex behind RefreshingClient — the wrapping build.go applies.
		{"refreshing-codex", NewRefreshingClient(NewOpenAICodex("k", "", ""), noopRefresh, noopFactory), true},
		// openai-responses is a codexClient behind renamedClient.
		{"openai-responses", NewOpenAIResponses("k", ""), true},
		// Defense in depth: renamedClient nested in RefreshingClient.
		{"refreshing-renamed-codex", NewRefreshingClient(NewOpenAIResponses("k", ""), noopRefresh, noopFactory), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientMirrorsToolImages(tc.c); got != tc.want {
				t.Errorf("ClientMirrorsToolImages(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A tool result containing an image must serialize the chat-completions
// `tool` message Content as a plain string (a note), never an array /
// image_url part — OpenAI-compatible servers reject images in a tool
// message (HTTP 400 "'content' field must be a string or an array of
// objects"). The image reaches vision models via the agent-loop mirror
// (mirrorToolImagesAsUser), which serializes it as image_url in a
// following user message.
func TestOpenAIToolImageContentIsString(t *testing.T) {
	c := NewOpenAI("token", "https://example.test").(*openaiClient)
	req, err := c.buildRequest(Request{
		Model: "vision-model",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "look"}}},
			{Role: RoleAssistant, Content: []Content{
				ToolCallBlock{ID: "call_1", Name: "fetch_image", Arguments: []byte(`{"url":"http://x/y.png"}`)},
			}},
			{Role: RoleTool, Content: []Content{
				ToolResultBlock{CallID: "call_1", Content: []Content{
					ImageBlock{MimeType: "image/png", Data: []byte("bytes")},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *oaiMessage
	for i := range req.Messages {
		if req.Messages[i].Role == "tool" {
			toolMsg = &req.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message produced")
	}
	s, ok := toolMsg.Content.(string)
	if !ok {
		t.Fatalf("tool message Content = %T, want string (images must not be inlined into a tool message)", toolMsg.Content)
	}
	// Image-only result: the note tells the model the image is coming next.
	if s == "" {
		t.Error("image-only tool result produced empty content; want a short note")
	}
}

// A text tool result is unchanged: plain string content, with an [error]
// marker when the result is an error.
func TestOpenAIToolTextContent(t *testing.T) {
	c := NewOpenAI("token", "https://example.test").(*openaiClient)
	req, err := c.buildRequest(Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleAssistant, Content: []Content{
				ToolCallBlock{ID: "call_1", Name: "run", Arguments: []byte(`{}`)},
			}},
			{Role: RoleTool, Content: []Content{
				ToolResultBlock{CallID: "call_1", IsError: true, Content: []Content{
					TextBlock{Text: "boom"},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *oaiMessage
	for i := range req.Messages {
		if req.Messages[i].Role == "tool" {
			toolMsg = &req.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message produced")
	}
	s, ok := toolMsg.Content.(string)
	if !ok {
		t.Fatalf("tool message Content = %T, want string", toolMsg.Content)
	}
	if s != "boom [error]" {
		t.Errorf("tool content = %q, want %q", s, "boom [error]")
	}
}
