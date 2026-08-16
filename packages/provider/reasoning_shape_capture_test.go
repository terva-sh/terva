package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The other half of the census: every capture site TAGS what it produces.
//
// The serializer census next door proves each shape has one owner, but it
// builds its blocks by hand. If a client stopped tagging, every one of those
// tests would still pass while the blocks coming off the wire quietly went
// untagged — and an untagged block now means "written before tagging existed",
// so the loader would back-fill it as a Codex item and hand it to the wrong
// serializer.
//
// So this asserts the tag on blocks that came from an actual stream, one per
// capturing client. Anthropic's two shapes are covered in
// anthropic_adaptive_thinking_test.go, which has to distinguish between them.

// chatReasoningFrames is a chat-completions stream carrying reasoning_content
// (DeepSeek, Kimi and the other OpenAI-compatible thinking endpoints).
const chatReasoningFrames = `data: {"choices":[{"delta":{"reasoning_content":"weighing the options"}}]}

data: {"choices":[{"delta":{"content":"done"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func firstReasoningBlock(t *testing.T, m Message) ReasoningBlock {
	t.Helper()
	for _, c := range m.Content {
		if rb, ok := c.(ReasoningBlock); ok {
			return rb
		}
	}
	t.Fatalf("no ReasoningBlock on the message: %+v", m.Content)
	return ReasoningBlock{}
}

func TestCodexCaptureTagsItsReasoning(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(codexSummaryFrames))}
	out := make(chan Event, 64)
	go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

	_, msg := collectReasoningDeltas(t, out)
	if got := firstReasoningBlock(t, msg).Shape; got != ReasoningShapeOpenAIResponses {
		t.Errorf("Shape = %q, want %q", got, ReasoningShapeOpenAIResponses)
	}
}

func TestChatCaptureTagsItsReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chatReasoningFrames))
	}))
	defer srv.Close()

	c := newOpenAICompat("openai-compatible", "k", srv.URL, "")
	evs, err := c.Stream(context.Background(), Request{Model: "qwen3.8-27b"})
	if err != nil {
		t.Fatal(err)
	}
	_, msg := collectReasoningDeltas(t, evs)

	rb := firstReasoningBlock(t, msg)
	if rb.Shape != ReasoningShapeOpenAIChat {
		t.Errorf("Shape = %q, want %q", rb.Shape, ReasoningShapeOpenAIChat)
	}
	if rb.Summary != "weighing the options" {
		t.Errorf("Summary = %q, want the reasoning_content text", rb.Summary)
	}
}

func TestGeminiCaptureTagsItsReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(geminiThoughtStream))
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-3-flash-preview"})
	if err != nil {
		t.Fatal(err)
	}
	_, msg := collectReasoningDeltas(t, evs)

	if got := firstReasoningBlock(t, msg).Shape; got != ReasoningShapeGeminiThoughtSummary {
		t.Errorf("Shape = %q, want %q", got, ReasoningShapeGeminiThoughtSummary)
	}
}

// The end-to-end statement of the bug this all exists to stop, on the path a
// user actually takes: a session runs on Anthropic, the user switches to
// Codex, and the transcript still holds Anthropic's blocks.
//
// Before the tags this sent {id:"", encrypted_content:"<Anthropic signature>"}
// — one vendor's seal handed to another as if it were its own — on every turn
// after the switch.
func TestAnthropicTranscriptReplayedToCodexCarriesNoAnthropicPayload(t *testing.T) {
	captured := ReasoningBlock{Summary: "the second read wins", Encrypted: "ANTHROPIC-SIG", Shape: ReasoningShapeAnthropicThinking}
	opaque := ReasoningBlock{Encrypted: "ANTHROPIC-SIG-2", Shape: ReasoningShapeAnthropicThinkingOpaque}

	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-sol",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "fix it"}}},
			{Role: RoleAssistant, Content: []Content{captured, opaque, TextBlock{Text: "fixed"}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "and now?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range wire.Input {
		if ri, ok := it.(codexReasoningItem); ok {
			t.Errorf("replayed an Anthropic block to the Codex wire: id=%q encrypted=%q",
				ri.ID, ri.EncryptedContent)
		}
	}
	// The turn itself must survive — only the foreign reasoning is dropped.
	var sawText bool
	for _, it := range wire.Input {
		if m, ok := it.(codexOutputMessage); ok {
			for _, ct := range m.Content {
				if strings.Contains(ct.Text, "fixed") {
					sawText = true
				}
			}
		}
	}
	if !sawText {
		t.Error("dropping the foreign reasoning also ate the assistant's answer")
	}
}

// The mirror, and the one with a privacy edge: Anthropic's Summary is the
// model's verbatim chain-of-thought, so replaying it to a chat provider hands
// one vendor the other's private deliberation, presented as this model's own
// prior words.
func TestAnthropicTranscriptReplayedToChatCarriesNoChainOfThought(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "fix it"}}},
			{Role: RoleAssistant, Content: []Content{
				ReasoningBlock{Summary: "the second read wins", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking},
				TextBlock{Text: "fixed"},
				ToolCallBlock{ID: "call_1", Name: "read", Arguments: []byte(`{}`)},
			}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "call_1", Content: []Content{TextBlock{Text: "ok"}}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Messages {
		if strings.Contains(m.ReasoningContent, "the second read wins") {
			t.Errorf("Anthropic chain-of-thought replayed as reasoning_content: %q", m.ReasoningContent)
		}
		if txt, ok := m.Content.(string); ok && strings.Contains(txt, "the second read wins") {
			t.Errorf("Anthropic chain-of-thought promoted into visible content: %q", txt)
		}
	}
}

// geminiSealedThoughtStream is a live-shaped Gemini 3 response: thought
// summaries, the answer, then a TRAILING part carrying the turn's signature and
// an empty text. Taken from a real gemini-3-flash-preview response, with the
// 7472-character signature truncated.
//
// 🪤 That trailing part is the whole point. terva read thoughtSignature only off
// functionCall parts, and every other part hit an `if part.Text == "" continue`
// before anything looked at the signature — so a text-only turn's seal was
// dropped on the floor.
const geminiSealedThoughtStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"**Analyzing Factorial Zeros**\n\nLegendre's formula gives the count.","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"The answer is 405."}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"Et8rCtwrARFNMg+6LhfTaJ6Gk3F8HAZ9gQg6hwzh"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":35,"candidatesTokenCount":2233,"thoughtsTokenCount":1643}}

`

func TestGeminiKeepsTheSealOnATextOnlyTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(geminiSealedThoughtStream))
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-3-flash-preview"})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var msg Message
	var usage Usage
	for ev := range evs {
		switch e := ev.(type) {
		case EventTextDelta:
			text.WriteString(e.Delta)
		case EventUsage:
			usage = e.Usage
		case EventDone:
			msg = e.Message
		}
	}

	rb := firstReasoningBlock(t, msg)
	if rb.Shape != ReasoningShapeGeminiThoughtSummary {
		t.Errorf("Shape = %q, want %q", rb.Shape, ReasoningShapeGeminiThoughtSummary)
	}
	if rb.Encrypted != "Et8rCtwrARFNMg+6LhfTaJ6Gk3F8HAZ9gQg6hwzh" {
		t.Errorf("Encrypted = %q, want the trailing thoughtSignature", rb.Encrypted)
	}
	if !strings.Contains(rb.Summary, "Legendre") {
		t.Errorf("Summary = %q, want the thought text", rb.Summary)
	}
	// The seal's empty text must not reach the answer, and the answer must not
	// reach the summary.
	if text.String() != "The answer is 405." {
		t.Errorf("answer text = %q", text.String())
	}
	if usage.ReasoningTokens != 1643 || !usage.ReasoningTokensKnown {
		t.Errorf("reasoning tokens = %d known=%v, want 1643 true", usage.ReasoningTokens, usage.ReasoningTokensKnown)
	}
}

// A turn that thinks without summarizing still gets sealed, and the seal is
// what is replayed — so the block has to survive with no readable half at all.
// Measured live: a short tool-call turn spent 66 thinking tokens and returned
// zero thought parts.
func TestGeminiKeepsASealWithNoSummary(t *testing.T) {
	frames := strings.Replace(geminiSealedThoughtStream,
		`{"text":"**Analyzing Factorial Zeros**\n\nLegendre's formula gives the count.","thought":true}`,
		`{"text":"","thought":true}`, 1)
	if frames == geminiSealedThoughtStream {
		t.Fatal("fixture no longer contains the thought part this test blanks")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(frames))
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-3-flash-preview"})
	if err != nil {
		t.Fatal(err)
	}
	_, msg := collectReasoningDeltas(t, evs)

	rb := firstReasoningBlock(t, msg)
	if rb.Summary != "" {
		t.Errorf("Summary = %q, want empty", rb.Summary)
	}
	if rb.Encrypted == "" {
		t.Error("the seal was dropped because there was no summary beside it")
	}
}
