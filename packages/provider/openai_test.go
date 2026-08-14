package provider

import (
	"strings"
	"testing"
)

// Client capabilities are declared through one explicit struct
// (ClientCapabilities) behind the named capabilityProvider interface,
// not a per-capability anonymous interface. Every OpenAI
// chat-completions provider (and the Responses/codex route) declares
// MirrorsToolImages — their wire formats can't carry images in a tool
// result. It's a WIRE-FORMAT fact; whether a given MODEL can see
// images is the separate per-model capability the loop checks
// (docs/plans/model-capabilities.md). Providers that carry tool
// images natively (Anthropic) must NOT declare it.
//
// Gemini was listed as native here and is not: its functionResponse
// carries text only, so it declares the mirror too. Measured live
// 2026-08-14 — a tool-returned image reached the model ONLY through
// the mirror; the native path answered about an image it never saw.
func TestClientCapabilitiesDeclared(t *testing.T) {
	cases := []struct {
		name       string
		client     Client
		wantImpl   bool // implements capabilityProvider
		wantMirror bool
	}{
		{"openai", NewOpenAI("k", ""), true, true},
		{"groq", NewGroq("k", ""), true, true},
		{"openai-codex", NewOpenAICodex("k", "", ""), true, true},
		{"deepseek", NewDeepSeek("k", ""), true, true},
		// Anthropic carries tool images natively (no mirror) but DOES declare a
		// capability now — ContinuesAssistantPrefill — so it implements the
		// interface; MirrorsToolImages stays false.
		{"anthropic", NewAnthropic("k", ""), true, false},
		// Gemini's functionResponse is text-only: it drops ImageBlocks on the
		// wire, so it needs the mirror. Declaring nothing here is what made a
		// tool-returned image vanish with no error at all.
		{"google", NewGemini("k", ""), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Probe through wrapper layers the way ClientCaps does — some
			// providers (deepseek, openrouter) sit behind a pollingUsageClient.
			_, impl := clientAs[capabilityProvider](tc.client)
			if impl != tc.wantImpl {
				t.Fatalf("implements capabilityProvider = %v, want %v", impl, tc.wantImpl)
			}
			if got := ClientCaps(tc.client).MirrorsToolImages; got != tc.wantMirror {
				t.Errorf("ClientCaps().MirrorsToolImages = %v, want %v", got, tc.wantMirror)
			}
		})
	}
}

// The capability MUST survive through the wrappers the build layer
// actually ships: openai-responses / google-vertex ride in renamedClient,
// and deepseek in pollingUsageClient. A direct type assertion on the
// outermost client returns false for these, so the agent loop uses
// ClientMirrorsToolImages, which unwraps recursively. This is the
// regression test for the inert-mirroring bug (deep-review Part B #7):
// the raw constructors opt in, but the wrapped clients that ship silently
// didn't.
func TestClientMirrorsToolImagesThroughWrappers(t *testing.T) {
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

		// openai-responses is a codexClient behind renamedClient — the
		// unwrap walk must still reach the codex capability.
		{"openai-responses", NewOpenAIResponses("k", ""), true},

		// Gemini needs the mirror (text-only functionResponse), and Vertex
		// ships that same client behind renamedClient. Built inline rather
		// than through NewVertex, which needs GOOGLE_CLOUD_PROJECT in the
		// environment and degrades to an unimplementedClient without it.
		{"raw-gemini", NewGemini("k", ""), true},
		{"google-vertex", &renamedClient{inner: NewGemini("k", ""), name: "google-vertex"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientMirrorsToolImages(tc.c); got != tc.want {
				t.Errorf("ClientMirrorsToolImages(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ContinuesAssistantPrefill is the inverse split of MirrorsToolImages: only
// Anthropic (Claude Messages) extends a trailing assistant message as a prefill,
// so only it opts in — the Stage "continue" gate. It must survive the wrapper
// walk the same way, so the OAuth client (which some builds wrap) still reports
// it. OpenAI/codex/deepseek treat a trailing assistant as history or reject it.
func TestClientContinuesAssistantPrefill(t *testing.T) {
	cases := []struct {
		name string
		c    Client
		want bool
	}{
		{"anthropic", NewAnthropic("k", ""), true},
		{"anthropic-oauth", NewAnthropicOAuth("t", ""), true},
		{"openai", NewOpenAI("k", ""), false},
		{"openai-codex", NewOpenAICodex("k", "", ""), false},
		{"deepseek", NewDeepSeek("k", ""), false},
		// A codexClient behind renamedClient stays false through the unwrap walk.
		{"openai-responses", NewOpenAIResponses("k", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientContinuesAssistantPrefill(tc.c); got != tc.want {
				t.Errorf("ClientContinuesAssistantPrefill(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A trailing assistant message is a prefill; Anthropic rejects one that ends in
// whitespace, so the converter right-trims its final text block — but only when
// the assistant message is genuinely last (the continue case), never when a
// user/tool/ephemeral message follows it.
func TestAnthropicTrimsTrailingAssistantPrefill(t *testing.T) {
	c := NewAnthropic("k", "").(*anthropicClient)

	// Trailing assistant (a prefill): the whitespace is trimmed.
	req, err := c.buildRequest(Request{Model: "claude-sonnet-4-5", Messages: []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "Tell me a story."}}},
		{Role: RoleAssistant, Content: []Content{TextBlock{Text: "The knight rode on,\n\n"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last message role = %q, want assistant", last.Role)
	}
	if got := last.Content[len(last.Content)-1].(anthTextBlock).Text; got != "The knight rode on," {
		t.Errorf("trailing assistant prefill not trimmed: %q", got)
	}

	// An assistant message followed by ephemeral context is NOT a prefill — the
	// ephemeral user block is last, so nothing is trimmed.
	req2, err := c.buildRequest(Request{
		Model:            "claude-sonnet-4-5",
		EphemeralContext: "It is raining.",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "Hi."}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Hello.\n"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := req2.Messages[len(req2.Messages)-1].Role; got != "user" {
		t.Fatalf("with ephemeral, last role = %q, want user", got)
	}
	// The assistant message keeps its trailing newline (it is not the prefill).
	for _, m := range req2.Messages {
		if m.Role == "assistant" {
			if got := m.Content[len(m.Content)-1].(anthTextBlock).Text; got != "Hello.\n" {
				t.Errorf("non-trailing assistant was trimmed: %q", got)
			}
		}
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

// DeepSeek V4 is marked text-only (its API rejects image_url), so a user
// message carrying an image must serialize as a plain string — the image
// dropped from the wire (it stays in the transcript for a vision model).
func TestDeepSeekDropsUserImage(t *testing.T) {
	c := innerOpenAI(NewDeepSeek("k", "")) // unwrap the usage-polling layer
	req, err := c.buildRequest(Request{
		Model: "deepseek-v4-pro",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{
				TextBlock{Text: "what's in this picture"},
				ImageBlock{MimeType: "image/png", Data: []byte("bytes")},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var userMsg *oaiMessage
	for i := range req.Messages {
		if req.Messages[i].Role == "user" {
			userMsg = &req.Messages[i]
		}
	}
	if userMsg == nil {
		t.Fatal("no user message produced")
	}
	s, ok := userMsg.Content.(string)
	if !ok {
		t.Fatalf("deepseek user content = %T, want string (image must be dropped; the API rejects image_url)", userMsg.Content)
	}
	if !strings.Contains(s, "what's in this picture") {
		t.Errorf("text content lost: %q", s)
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
