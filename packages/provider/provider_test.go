package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEParse(t *testing.T) {
	r := strings.NewReader("event: foo\ndata: {\"a\":1}\n\ndata: hello\ndata: world\n\n")
	s := newSSEStream(io.NopCloser(r), "test")
	ch := s.Events()

	e := <-ch
	if e.Event != "foo" || e.Data != `{"a":1}` {
		t.Fatalf("event 1: %+v", e)
	}
	e = <-ch
	if e.Event != "" || e.Data != "hello\nworld" {
		t.Fatalf("event 2: %+v", e)
	}
	if _, ok := <-ch; ok {
		t.Fatalf("channel not closed")
	}
	if err := s.Err(); err != nil {
		t.Fatalf("clean stream reported err=%v", err)
	}
}

// A stream whose last event lacks its trailing blank line still delivers that
// event, and a CRLF-framed stream parses — both bufio.Scanner behaviors the
// lineframe migration had to preserve.
func TestSSEParseTrailingEventAndCRLF(t *testing.T) {
	s := newSSEStream(io.NopCloser(strings.NewReader("event: a\r\ndata: one\r\n\r\ndata: two")), "test")
	ch := s.Events()

	if e := <-ch; e.Event != "a" || e.Data != "one" {
		t.Fatalf("crlf event: %+v", e)
	}
	if e := <-ch; e.Data != "two" {
		t.Fatalf("unterminated final event: %+v", e)
	}
	if _, ok := <-ch; ok {
		t.Fatalf("channel not closed")
	}
	if err := s.Err(); err != nil {
		t.Fatalf("clean stream reported err=%v", err)
	}
}

// An over-limit line aborts the stream with a PERMANENT error. Retrying it
// would re-read the identical oversized line and re-pay the input tokens, so
// Transient must be false — the regression this guards is a silent flip back
// to the transient stream-death path.
func TestSSEOverLimitLineIsPermanent(t *testing.T) {
	body := "data: ok\n\ndata: " + strings.Repeat("x", maxSSELineBytes+1) + "\n\n"
	s := newSSEStream(io.NopCloser(strings.NewReader(body)), "test")
	ch := s.Events()

	if e := <-ch; e.Data != "ok" {
		t.Fatalf("first event: %+v", e)
	}
	if e, ok := <-ch; ok {
		t.Fatalf("oversized line was delivered, not rejected: %+v", e)
	}
	err := s.Err()
	if err == nil {
		t.Fatal("oversized line ended the stream silently")
	}
	if !errors.Is(err, ErrStreamLimit) {
		t.Fatalf("want ErrStreamLimit, got %v", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ProviderError, got %T", err)
	}
	if pe.Transient {
		t.Fatal("over-limit line marked transient: the retry loop will burn its budget re-reading it")
	}
	if !strings.Contains(pe.Msg, "10 MiB") {
		t.Fatalf("error should name the limit, got %q", pe.Msg)
	}
}

// A transport failure mid-stream is transient and carries its cause, rather
// than being laundered into a generic stream-death by a discarded Scanner err.
func TestSSEReadErrorIsTransientAndWrapped(t *testing.T) {
	boom := errors.New("connection reset by peer")
	r := io.MultiReader(strings.NewReader("data: one\n\ndata: par"), errReader{boom})
	s := newSSEStream(io.NopCloser(r), "test")
	ch := s.Events()

	if e := <-ch; e.Data != "one" {
		t.Fatalf("first event: %+v", e)
	}
	if e, ok := <-ch; ok {
		t.Fatalf("half-read event was flushed: %+v", e)
	}
	err := s.Err()
	if !errors.Is(err, boom) {
		t.Fatalf("read error not wrapped, got %v", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || !pe.Transient {
		t.Fatalf("transport failure should be transient, got %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// closeUnblocks asserts that Close returns promptly, i.e. that the reader
// goroutine was freed rather than stranded for the life of the process.
func closeUnblocks(t *testing.T, s *sseStream) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: the reader goroutine is stranded")
	}
}

// Parking spot 1: the reader blocks on the buffered send once the client stops
// draining — which is what every cancelled turn does. Closing the body cannot
// free a goroutine blocked on a channel send; only a receiver can.
func TestSSEStreamCloseUnblocksParkedSend(t *testing.T) {
	// Far more events than the 16-slot buffer, and we read only one.
	body := strings.Repeat("data: x\n\n", 200)
	s := newSSEStream(io.NopCloser(strings.NewReader(body)), "test")
	if e := <-s.Events(); e.Data != "x" {
		t.Fatalf("first event: %+v", e)
	}
	closeUnblocks(t, s)
}

// Parking spot 2: the reader blocks in Read on a connection that never
// delivers. Draining cannot free it; closing the body must.
func TestSSEStreamCloseUnblocksParkedRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	s := newSSEStream(pr, "test") // nothing is ever written: the reader parks in Read
	closeUnblocks(t, s)
}

func TestModelCatalog(t *testing.T) {
	if len(Catalog) == 0 {
		t.Fatal("empty catalog")
	}
	if _, err := FindModel("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatal(err)
	}
	if _, err := FindModel("openai", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	if _, err := FindModel("", "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestComputeCost(t *testing.T) {
	m, _ := FindModel("anthropic", "claude-sonnet-4-5")
	cost := ComputeCost(m, Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	want := m.PriceInput + m.PriceOutput
	if cost != want {
		t.Fatalf("cost=%v want=%v", cost, want)
	}
}

func TestAnthropicErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"auth","message":"bad key"}}`))
	}))
	defer srv.Close()

	c := NewAnthropic("x", srv.URL)
	_, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-4-5"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 err, got %v", err)
	}
}

func TestOpenAIErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	defer srv.Close()

	c := NewOpenAI("x", srv.URL)
	_, err := c.Stream(context.Background(), Request{Model: "gpt-5"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400 err, got %v", err)
	}
}

func TestAnthropicAdaptiveThinking(t *testing.T) {
	c := NewAnthropic("x", "").(*anthropicClient)
	temp := float32(0.7)

	// Opus 4.8 -> adaptive thinking, effort set, no budget, no temperature.
	wire, err := c.buildRequest(Request{
		Model:        "claude-opus-4-8",
		Reasoning:    "high",
		ReasoningSet: true,
		Temperature:  &temp,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Thinking == nil || wire.Thinking.Type != "adaptive" {
		t.Fatalf("want adaptive thinking, got %+v", wire.Thinking)
	}
	if wire.Thinking.BudgetTokens != 0 {
		t.Fatalf("adaptive must not send budget_tokens, got %d", wire.Thinking.BudgetTokens)
	}
	if wire.OutputConfig == nil || wire.OutputConfig.Effort != "high" {
		t.Fatalf("want effort=high, got %+v", wire.OutputConfig)
	}
	if wire.Temperature != nil {
		t.Fatalf("adaptive must drop temperature, got %v", *wire.Temperature)
	}

	// maximum -> xhigh effort.
	wire, err = c.buildRequest(Request{
		Model:        "claude-opus-4-8",
		Reasoning:    "maximum",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.OutputConfig == nil || wire.OutputConfig.Effort != "xhigh" {
		t.Fatalf("want effort=xhigh, got %+v", wire.OutputConfig)
	}

	// max -> native max effort, a separate tier above xhigh on adaptive models.
	wire, err = c.buildRequest(Request{
		Model:        "claude-opus-4-8",
		Reasoning:    "max",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.OutputConfig == nil || wire.OutputConfig.Effort != "max" {
		t.Fatalf("want effort=max, got %+v", wire.OutputConfig)
	}

	// Opus 4.5 -> budget-based thinking, no output_config, temperature kept.
	wire, err = c.buildRequest(Request{
		Model:        "claude-opus-4-5",
		Reasoning:    "high",
		ReasoningSet: true,
		Temperature:  &temp,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Thinking == nil || wire.Thinking.Type != "enabled" || wire.Thinking.BudgetTokens <= 0 {
		t.Fatalf("want budget thinking, got %+v", wire.Thinking)
	}
	if wire.OutputConfig != nil {
		t.Fatalf("budget models must not send output_config, got %+v", wire.OutputConfig)
	}
	if wire.Temperature == nil || *wire.Temperature != temp {
		t.Fatalf("budget model should keep temperature, got %v", wire.Temperature)
	}
}

// TestAnthropicClampsMaxTokensToModelCap guards the stale-budget 400: a
// per-turn MaxTokens sized for a high-cap model (Agent.MaxTokens is pinned at
// build time and NOT refreshed by Agent.SetModel) must be clamped to the
// resolved model's own MaxOutput, or a /model switch to a lower-cap model
// sends the old budget and Anthropic rejects it ("max_tokens: 128000 > 64000").
func TestAnthropicClampsMaxTokensToModelCap(t *testing.T) {
	c := NewAnthropic("x", "").(*anthropicClient)

	// sonnet-4-5 caps output at 64000; a stale 128000 budget must clamp down.
	wire, err := c.buildRequest(Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 128000,
		Messages:  []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.MaxTokens != 64000 {
		t.Fatalf("want max_tokens clamped to 64000, got %d", wire.MaxTokens)
	}

	// A budget already within the cap is left untouched.
	wire, err = c.buildRequest(Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 32000,
		Messages:  []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.MaxTokens != 32000 {
		t.Fatalf("want max_tokens preserved at 32000, got %d", wire.MaxTokens)
	}
}

func TestAnthropicBuildRequestStripsAssistantImages(t *testing.T) {
	c := NewAnthropic("x", "").(*anthropicClient)
	wire, err := c.buildRequest(Request{
		Model: "claude-sonnet-4-5",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "make an image"}}},
			{Role: RoleAssistant, Content: []Content{
				TextBlock{Text: "done"},
				ImageBlock{MimeType: "image/png", Data: []byte("png")},
				TextBlock{Text: "Saved image: `terva-gemini-image-x.png`"},
			}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 3 {
		t.Fatalf("messages=%d", len(wire.Messages))
	}
	assistant := wire.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("role=%q", assistant.Role)
	}
	if len(assistant.Content) != 2 {
		t.Fatalf("assistant content=%+v", assistant.Content)
	}
	for _, b := range assistant.Content {
		if _, ok := b.(anthImageBlock); ok {
			t.Fatalf("assistant image block was not stripped: %+v", assistant.Content)
		}
	}
}

func TestAnthropicStreamHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		write("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		write("event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		write("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		write("event: content_block_stop\ndata: {\"index\":0}\n\n")
		write("event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		write("event: message_stop\ndata: {}\n\n")
	}))
	defer srv.Close()

	c := NewAnthropic("x", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	var gotText string
	var done EventDone
	for ev := range evs {
		switch e := ev.(type) {
		case EventTextDelta:
			gotText += e.Delta
		case EventDone:
			done = e
		}
	}
	if gotText != "hi" {
		t.Fatalf("text=%q", gotText)
	}
	if done.Stop != StopEnd {
		t.Fatalf("stop=%v", done.Stop)
	}
}

func TestOpenAICompatAnthropicReasoningEffort(t *testing.T) {
	c := innerOpenAI(NewOpenRouter("token", "")) // unwrap the usage-polling layer
	wire, err := c.buildRequest(Request{
		Model:        "anthropic/claude-opus-4.8",
		Reasoning:    "maximum",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.ReasoningEffort != "xhigh" {
		t.Fatalf("want xhigh for adaptive Anthropic model over OpenAI-compatible wire, got %q", wire.ReasoningEffort)
	}

	wire, err = c.buildRequest(Request{
		Model:        "gpt-5.1",
		Reasoning:    "maximum",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.ReasoningEffort != "high" {
		t.Fatalf("want generic OpenAI-compatible maximum clamped to high, got %q", wire.ReasoningEffort)
	}
}

// Kimi rejects an assistant message carrying neither text nor tool calls
// ("assistant must not be empty"). That invariant is what this test has always
// guarded, and it still holds — but the turn is now kept rather than dropped.
//
// A reasoning-only turn used to be skipped outright, which tore a hole in the
// replayed history exactly where an answer had been: a server that classifies
// a whole reply as reasoning (a thinking channel that never closes) produced
// turns the model could no longer see, and it apologised for lapses it had no
// record of. The reasoning is that turn's only substance, so it is promoted
// into the visible content — which satisfies Kimi too, because the message is
// no longer empty.
func TestOpenAIBuildRequestPromotesReasoningOnlyAssistantMessages(t *testing.T) {
	c := NewKimi("token", "").(*openaiClient)
	wire, err := c.buildRequest(Request{
		Model: "kimi-for-coding",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "first"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: "thinking only", Shape: ReasoningShapeOpenAIChat}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "second"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The original invariant, unchanged: nothing empty ever reaches Kimi.
	for i, msg := range wire.Messages {
		if msg.Role == "assistant" && msg.Content == nil && len(msg.ToolCalls) == 0 {
			t.Fatalf("message %d is empty assistant: %+v", i, msg)
		}
	}
	if got := len(wire.Messages); got != 3 {
		t.Fatalf("messages=%d want 3: the reasoning-only turn must survive replay", got)
	}
	if got, _ := wire.Messages[1].Content.(string); got != "thinking only" {
		t.Fatalf("assistant content=%q want the promoted reasoning", got)
	}
}

// An assistant turn with no substance at all still has nothing to promote, so
// the skip survives for the case that actually trips Kimi's validator.
func TestOpenAIBuildRequestSkipsTrulyEmptyAssistantMessages(t *testing.T) {
	c := NewKimi("token", "").(*openaiClient)
	wire, err := c.buildRequest(Request{
		Model: "kimi-for-coding",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "first"}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "   "}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "second"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(wire.Messages); got != 2 {
		t.Fatalf("messages=%d want 2 after skipping the empty assistant", got)
	}
}

func TestOpenAIStreamHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		write("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\n\n")
		write("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		write("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAI("x", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	var gotText string
	var done EventDone
	for ev := range evs {
		switch e := ev.(type) {
		case EventTextDelta:
			gotText += e.Delta
		case EventDone:
			done = e
		}
	}
	if gotText != "hello" {
		t.Fatalf("text=%q", gotText)
	}
	if done.Stop != StopEnd {
		t.Fatalf("stop=%v", done.Stop)
	}
}

// TestOpenAIStreamDeathMidToolCall simulates a connection that dies
// mid-stream after a partial tool call and BEFORE the [DONE] terminal
// frame. The handler writes a tool-call delta, then returns (closing the
// connection) without ever sending [DONE]. The client must surface this
// as EventDone{Stop: StopError} with a non-nil error wrapping
// io.ErrUnexpectedEOF, not a clean StopEnd — otherwise the truncated
// message is silently accepted and neither retry nor rescue fires.
func TestOpenAIStreamDeathMidToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		// Announce a tool call and stream partial arguments, then return
		// without [DONE] — the http server closes the body, the client's
		// raw SSE channel closes mid-stream.
		write("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"do_thing\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n")
		write("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"partial\\\":\"}}]},\"finish_reason\":null}]}\n\n")
		// Connection dies here: no more frames, no [DONE].
	}))
	defer srv.Close()

	c := NewOpenAI("x", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	var done EventDone
	var sawToolStart bool
	for ev := range evs {
		switch e := ev.(type) {
		case EventToolStart:
			sawToolStart = true
		case EventDone:
			done = e
		}
	}
	if !sawToolStart {
		t.Fatalf("expected a tool-start event before the connection died")
	}
	if done.Stop != StopError {
		t.Fatalf("stop=%v, want StopError on mid-stream connection death", done.Stop)
	}
	if done.Err == nil {
		t.Fatalf("expected a non-nil error on mid-stream connection death")
	}
	if !errors.Is(done.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want it to wrap io.ErrUnexpectedEOF", done.Err)
	}
}

func TestDiscoverOpenRouter(t *testing.T) {
	const body = `{"data":[
		{"id":"x/full","pricing":{"prompt":"0.000003","completion":"0.000015"},
		 "context_length":200000,"top_provider":{"max_completion_tokens":64000},
		 "supported_parameters":["reasoning"]},
		{"id":"x/fallback","top_provider":{"context_length":128000}},
		{"id":""}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := DiscoverOpenRouter(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 { // empty id dropped
		t.Fatalf("want 2 models, got %d", len(models))
	}
	// per-token USD -> per-1M; reasoning from supported_parameters.
	if m := models[0]; m.Provider != "openrouter" || m.ContextWindow != 200000 ||
		m.MaxOutput != 64000 || !m.Reasoning || m.PriceInput != 3 || m.PriceOutput != 15 {
		t.Errorf("model[0]: %+v", m)
	}
	// context falls back to top_provider; no reasoning.
	if m := models[1]; m.ContextWindow != 128000 || m.MaxOutput != 0 || m.Reasoning {
		t.Errorf("model[1]: %+v", m)
	}
}

// TestOpenAIOmitsZeroMaxTokens guards against sending max_tokens: 0 for
// discovered models that don't advertise an output cap (MaxOutput == 0).
func TestOpenAIOmitsZeroMaxTokens(t *testing.T) {
	SetLiveModels([]Model{
		{Provider: "openrouter", ID: "vendor/no-cap", DisplayName: "No Cap"},
		{Provider: "openrouter", ID: "vendor/reason-no-cap", DisplayName: "Reason No Cap", Reasoning: true},
		{Provider: "openrouter", ID: "vendor/capped", DisplayName: "Capped", MaxOutput: 4096},
	})
	defer SetLiveModels(nil)

	c := NewOpenAI("x", "").(*openaiClient)

	got, err := c.buildRequest(Request{Model: "vendor/no-cap"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != nil {
		t.Errorf("expected max_tokens omitted, got %d", *got.MaxTokens)
	}

	got, err = c.buildRequest(Request{Model: "vendor/reason-no-cap"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxCompletionTok != nil {
		t.Errorf("expected max_completion_tokens omitted, got %d", *got.MaxCompletionTok)
	}

	got, err = c.buildRequest(Request{Model: "vendor/capped"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 4096 {
		t.Errorf("expected max_tokens 4096, got %v", got.MaxTokens)
	}
}
