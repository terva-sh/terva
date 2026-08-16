package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The live half of reasoning summaries: the same text that lands in a
// ReasoningBlock has to reach the caller WHILE the turn runs, because a
// progress headline read after the fact describes work the user already
// watched finish.

// collectReasoningDeltas drains a stream and returns the concatenated reasoning
// deltas alongside the finished message.
func collectReasoningDeltas(t *testing.T, evs <-chan Event) (string, Message) {
	t.Helper()
	var deltas strings.Builder
	var msg Message
	for ev := range evs {
		switch e := ev.(type) {
		case EventReasoningDelta:
			deltas.WriteString(e.Delta)
		case EventDone:
			msg = e.Message
		}
	}
	return deltas.String(), msg
}

func summaryOf(t *testing.T, m Message) string {
	t.Helper()
	for _, c := range m.Content {
		if rb, ok := c.(ReasoningBlock); ok {
			return rb.Summary
		}
	}
	return ""
}

// The codex summary streams in parts, and the SEPARATOR between parts is the
// thing a live consumer needs most: it is what tells one headline from the
// next. Asserting the deltas rebuild the assembled summary EXACTLY covers both
// the text and the boundary — drop the "\n\n" emit and the two headlines fuse.
func TestCodexReasoningDeltasRebuildTheAssembledSummary(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(codexSummaryFrames))}
	out := make(chan Event, 64)
	go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

	deltas, msg := collectReasoningDeltas(t, out)
	want := summaryOf(t, msg)
	if want == "" {
		t.Fatal("no ReasoningBlock on the finished message; the fixture should carry one")
	}
	if deltas != want {
		t.Errorf("live deltas do not rebuild the summary:\n deltas = %q\n block  = %q", deltas, want)
	}
	// Non-vacuity: the fixture has two sections, so a passing test must have
	// carried a boundary rather than a single unbroken run.
	if !strings.Contains(deltas, "\n\n") {
		t.Errorf("no part boundary in the delta stream: %q", deltas)
	}
}

// geminiThoughtStream is a gemini SSE response carrying a thought-summary part
// (thought=true) and an ordinary answer part.
const geminiThoughtStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Checking the config","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":" then the handler","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"All set."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}

`

// terva asks gemini for thought summaries on every generation-3 request
// (includeThoughts), so dropping them on arrival meant paying to generate and
// return text that was then discarded. They must reach the caller live AND land
// on the message — but never as reply text, which is the part that would put
// the model's thinking back into its own mouth on the following turn.
func TestGeminiThoughtPartsBecomeReasoningNotText(t *testing.T) {
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

	var text strings.Builder
	var deltas strings.Builder
	var msg Message
	for ev := range evs {
		switch e := ev.(type) {
		case EventTextDelta:
			text.WriteString(e.Delta)
		case EventReasoningDelta:
			deltas.WriteString(e.Delta)
		case EventDone:
			msg = e.Message
		}
	}

	const wantThought = "Checking the config then the handler"
	if deltas.String() != wantThought {
		t.Errorf("live reasoning deltas = %q, want %q", deltas.String(), wantThought)
	}
	if got := summaryOf(t, msg); got != wantThought {
		t.Errorf("ReasoningBlock.Summary = %q, want %q", got, wantThought)
	}
	// The two halves that must NOT happen: thinking leaking into the reply, and
	// the answer going missing because it was mistaken for thinking.
	if got := text.String(); got != "All set." {
		t.Errorf("answer text = %q, want %q", got, "All set.")
	}
	for _, ct := range msg.Content {
		if tb, ok := ct.(TextBlock); ok && strings.Contains(tb.Text, "Checking the config") {
			t.Errorf("thought text leaked into a TextBlock: %q", tb.Text)
		}
	}
}
