package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Two conventions name the thinking channel on the chat-completions wire.
// DeepSeek and Kimi send `reasoning_content`, and terva has always read that
// one. OpenRouter and many local servers send `reasoning`, and terva read
// nothing from them: the model appeared not to think at all, at every effort
// level. These tests hold the second name open.

func streamChatFrames(t *testing.T, frames string) (string, Message) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(frames))
	}))
	defer srv.Close()

	c := newOpenAICompat("openai-compatible", "k", srv.URL, "")
	evs, err := c.Stream(context.Background(), Request{Model: "qwen3.8-27b"})
	if err != nil {
		t.Fatal(err)
	}
	return collectReasoningDeltas(t, evs)
}

func textOf(t *testing.T, m Message) string {
	t.Helper()
	for _, c := range m.Content {
		if tb, ok := c.(TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}

// The whole point of gap 1: a server that only ever says `reasoning`.
func TestChatCaptureReadsBareReasoningField(t *testing.T) {
	const frames = `data: {"choices":[{"delta":{"reasoning":"weighing the options"}}]}

data: {"choices":[{"delta":{"content":"done"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	deltas, msg := streamChatFrames(t, frames)
	if deltas != "weighing the options" {
		t.Errorf("reasoning deltas = %q, want the `reasoning` text", deltas)
	}
	rb := firstReasoningBlock(t, msg)
	if rb.Summary != "weighing the options" {
		t.Errorf("Summary = %q, want the `reasoning` text", rb.Summary)
	}
	// It is the same channel by another name, so it carries the same shape
	// tag. A different tag here would route it to the wrong serializer.
	if rb.Shape != ReasoningShapeOpenAIChat {
		t.Errorf("Shape = %q, want %q", rb.Shape, ReasoningShapeOpenAIChat)
	}
}

// A server that mirrors its thinking into both names must not have it
// recorded twice.
func TestChatCapturePrefersReasoningContentOverReasoning(t *testing.T) {
	const frames = `data: {"choices":[{"delta":{"reasoning_content":"the thought","reasoning":"the thought"}}]}

data: {"choices":[{"delta":{"content":"done"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	deltas, _ := streamChatFrames(t, frames)
	if deltas != "the thought" {
		t.Errorf("reasoning deltas = %q, want the thought exactly once", deltas)
	}
}

// The stream decoder drops any chunk it cannot unmarshal. Reading a second
// reasoning field therefore put the visible answer at risk: a server that
// uses `reasoning` for a non-string would have taken the text in the same
// chunk down with it. The thinking may be lost. The answer may not be.
func TestChatCaptureSurvivesNonStringReasoning(t *testing.T) {
	const frames = `data: {"choices":[{"delta":{"reasoning":{"effort":"high"},"content":"the answer"}}]}

data: {"choices":[{"delta":{"reasoning":null,"content":" continues"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	deltas, msg := streamChatFrames(t, frames)
	if got := textOf(t, msg); got != "the answer continues" {
		t.Errorf("text = %q, want the answer intact despite the odd reasoning shape", got)
	}
	if deltas != "" {
		t.Errorf("reasoning deltas = %q, want nothing from a non-string field", deltas)
	}
}
