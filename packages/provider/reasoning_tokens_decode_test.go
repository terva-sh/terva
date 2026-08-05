package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Each backend has to actually populate the field, and the wire shapes differ
// enough that a unit test on Usage alone proves nothing. These drive the real
// decoders with the payloads the providers really send.
func usageFromStream(t *testing.T, run func(*http.Response, chan Event)) Usage {
	t.Helper()
	var got Usage
	out := make(chan Event, 32)
	run(nil, out)
	for ev := range out {
		if u, ok := ev.(EventUsage); ok {
			got = u.Usage
		}
	}
	return got
}

func TestCodexReportsReasoningTokens(t *testing.T) {
	c := &codexClient{}
	frame := `data: {"type":"response.completed","response":{"status":"completed",` +
		`"usage":{"input_tokens":1000,"output_tokens":700,` +
		`"output_tokens_details":{"reasoning_tokens":512}}}}` + "\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)
	})
	if !got.ReasoningTokensKnown {
		t.Fatal("codex reports output_tokens_details but the decoder did not mark it known")
	}
	if got.ReasoningTokens != 512 {
		t.Errorf("ReasoningTokens = %d, want 512", got.ReasoningTokens)
	}
	// The subset invariant, at the decoder rather than in the abstract.
	if got.ReasoningTokens > got.OutputTokens {
		t.Errorf("reasoning %d exceeds output %d", got.ReasoningTokens, got.OutputTokens)
	}
}

// A response with no details block must stay UNKNOWN rather than reporting a
// measured zero — the distinction the flag exists to carry.
func TestCodexWithoutDetailsStaysUnknown(t *testing.T) {
	c := &codexClient{}
	frame := `data: {"type":"response.completed","response":{"status":"completed",` +
		`"usage":{"input_tokens":1000,"output_tokens":700}}}` + "\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)
	})
	if got.ReasoningTokensKnown {
		t.Error("an absent output_tokens_details was read as a measured zero")
	}
	if got.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d, want 0", got.ReasoningTokens)
	}
}

// Gemini already parsed thoughtsTokenCount and folded it into OutputTokens, so
// the split existed on the wire and was thrown away. It must stay folded in —
// it is billed at the output rate — while also being reported.
func TestGeminiReportsThoughtsAsReasoningTokens(t *testing.T) {
	c := &geminiClient{}
	frame := `data: {"candidates":[{"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":200,` +
		`"thoughtsTokenCount":300,"totalTokenCount":1400}}` + "\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "gemini-3-pro-preview"}, out)
	})
	if !got.ReasoningTokensKnown {
		t.Fatal("gemini reports thoughtsTokenCount but it was not marked known")
	}
	if got.ReasoningTokens != 300 {
		t.Errorf("ReasoningTokens = %d, want 300", got.ReasoningTokens)
	}
	// Still counted in output: 200 answer + 300 thoughts.
	if got.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500 — thoughts must stay INSIDE output, "+
			"since they are billed at the output rate", got.OutputTokens)
	}
}

func TestOpenAICompatReportsReasoningTokens(t *testing.T) {
	c := &openaiClient{}
	frame := `data: {"choices":[{"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":700,` +
		`"completion_tokens_details":{"reasoning_tokens":450}}}` + "\n\n" +
		"data: [DONE]\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "gpt-5.5"}, out)
	})
	if !got.ReasoningTokensKnown {
		t.Fatal("completion_tokens_details was present but not marked known")
	}
	if got.ReasoningTokens != 450 {
		t.Errorf("ReasoningTokens = %d, want 450", got.ReasoningTokens)
	}
}

// Most OpenAI-compatible gateways omit the details block entirely. That is an
// absence of information, not a zero.
func TestOpenAICompatWithoutDetailsStaysUnknown(t *testing.T) {
	c := &openaiClient{}
	frame := `data: {"choices":[{"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":700}}` + "\n\n" +
		"data: [DONE]\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "gpt-5.5"}, out)
	})
	if got.ReasoningTokensKnown {
		t.Error("a gateway that omits completion_tokens_details was read as reporting zero")
	}
}

// Anthropic is the case the known-flag exists for: thinking rides inside
// output_tokens with no separate count, so its usage must never claim to know.
func TestAnthropicNeverClaimsToKnowReasoningTokens(t *testing.T) {
	c := &anthropicClient{}
	frame := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":1}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":700}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	got := usageFromStream(t, func(_ *http.Response, out chan Event) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(frame))}
		go c.runStream(context.Background(), resp, Request{Model: "claude-opus-4-1-20250805"}, out)
	})
	if got.ReasoningTokensKnown {
		t.Error("anthropic does not break reasoning out, so its usage must not claim to know")
	}
	if got.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d, want 0", got.ReasoningTokens)
	}
}
