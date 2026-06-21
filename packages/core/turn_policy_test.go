package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestClassifyRecoverable(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		want       bool
		wantPrefix string
	}{
		{"nil", nil, false, ""},
		{"typed 401", &provider.ProviderError{Provider: "anthropic", Status: 401, Msg: "bad key"}, true, "authentication failed"},
		{"typed 429", &provider.ProviderError{Provider: "openai", Status: 429, Msg: "slow down"}, true, "rate limited"},
		{"typed 503", &provider.ProviderError{Provider: "kimi", Status: 503, Msg: "overloaded"}, true, "provider unavailable"},
		{"typed transient stream death", provider.NewAPIError("anthropic", "stream died", true), true, "network failure"},
		{"typed 400 validation", &provider.ProviderError{Provider: "openai", Status: 400, Msg: "bad request"}, false, ""},
		{"typed 413 excluded", &provider.ProviderError{Provider: "openai", Status: 413, Msg: "too big"}, false, ""},
		{"prose rate limit", errors.New("openai: http 429: too many requests"), true, "rate limited"},
		{"prose timeout", errors.New("request timeout talking upstream"), true, "network failure"},
		{"plain error", errors.New("model produced garbage"), false, ""},
	}
	for _, c := range cases {
		got, reason := ClassifyRecoverable(c.err)
		if got != c.want {
			t.Errorf("%s: recoverable = %v, want %v", c.name, got, c.want)
			continue
		}
		if c.want && !strings.HasPrefix(reason, c.wantPrefix) {
			t.Errorf("%s: reason = %q, want prefix %q", c.name, reason, c.wantPrefix)
		}
	}
}

func TestIsPayloadTooLargeError(t *testing.T) {
	if !IsPayloadTooLargeError(&provider.ProviderError{Provider: "openai", Status: 413, Msg: "big"}) {
		t.Error("typed 413 not detected")
	}
	if IsPayloadTooLargeError(&provider.ProviderError{Provider: "openai", Status: 500, Msg: "big"}) {
		t.Error("typed 500 misdetected as 413")
	}
	if !IsPayloadTooLargeError(errors.New("anthropic: payload too large")) {
		t.Error("prose 413 not detected")
	}
	if IsPayloadTooLargeError(nil) {
		t.Error("nil misdetected")
	}
}

func TestExtractFailedProvider(t *testing.T) {
	if got := ExtractFailedProvider(&provider.ProviderError{Provider: "kimi", Status: 500, Msg: "x"}); got != "kimi" {
		t.Errorf("typed provider = %q, want kimi", got)
	}
	if got := ExtractFailedProvider(errors.New("anthropic: http 500: boom")); got != "anthropic" {
		t.Errorf("prose provider = %q, want anthropic", got)
	}
	if got := ExtractFailedProvider(errors.New("something else entirely")); got != "" {
		t.Errorf("unknown provider = %q, want empty", got)
	}
}

// policyFakeClient fails the first prompt with HTTP 413, serves the
// compaction summary, then succeeds the retried prompt.
type policyFakeClient struct {
	calls int32
}

func (c *policyFakeClient) Name() string { return "policy-fake" }

func (c *policyFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	if call == 1 {
		return nil, &provider.ProviderError{Provider: "policy-fake", Status: 413, Msg: "request entity too large"}
	}
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "policy-fake", Model: req.Model}
		out <- provider.EventTextDelta{Delta: "ok"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func TestPromptWithPolicyCompactsAndRetriesOn413(t *testing.T) {
	client := &policyFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	// Seed enough transcript that Compact has something to summarize
	// beyond its keep-tail.
	seed := make([]provider.Message, 0, 8)
	for range 4 {
		seed = append(seed,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		)
	}
	a.SetMessages(seed)

	var events []string
	err := a.PromptWithPolicy(context.Background(), "hello", nil, func(ev AgentEvent) {
		events = append(events, ev.Type())
	})
	if err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	// Call 1: 413. Call 2: compact summary. Call 3: retried prompt.
	if got := atomic.LoadInt32(&client.calls); got != 3 {
		t.Fatalf("Stream calls = %d; want 3 (413, compact, retry)", got)
	}
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "compact_start") || !strings.Contains(joined, "compact_end") {
		t.Fatalf("compact events missing from stream: %v", events)
	}
	// The compaction replaced the transcript with a summary + tail.
	msgs := a.Messages()
	if len(msgs) == 0 || msgs[0].Meta["compaction"] != "true" {
		t.Fatalf("transcript head is not a compaction summary; meta=%v", msgs[0].Meta)
	}
}

func TestCanCompact(t *testing.T) {
	a := NewAgent(nil, "fake-model", "system", Registry{})
	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	cases := []struct {
		n, keepTail int
		want        bool
	}{
		{0, 4, false}, // empty
		{4, 4, false}, // entirely keep-tail
		{3, 4, false}, // shorter than keep-tail
		{5, 4, true},  // one message beyond keep-tail
		{10, 4, true}, // comfortably compactable
		{5, -1, true}, // negative keepTail clamps to 0
		{0, 0, false}, // nothing at all
	}
	for _, c := range cases {
		seed := make([]provider.Message, c.n)
		for i := range seed {
			seed[i] = msg("m")
		}
		a.SetMessages(seed)
		if got := a.CanCompact(c.keepTail); got != c.want {
			t.Errorf("CanCompact(n=%d, keepTail=%d) = %v; want %v", c.n, c.keepTail, got, c.want)
		}
	}
}

func TestCompactReturnsNothingToCompactSentinel(t *testing.T) {
	// nil client is never reached: both paths return before any provider
	// call, which is exactly the contract we want for an opportunistic
	// auto-compact (no wasted model round-trip).
	a := NewAgent(nil, "fake-model", "system", Registry{})

	// Empty transcript.
	if _, err := a.Compact(context.Background(), AutoCompactKeepTail, nil); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("Compact(empty) err = %v; want ErrNothingToCompact", err)
	}

	// Transcript entirely covered by keep-tail.
	seed := make([]provider.Message, AutoCompactKeepTail)
	for i := range seed {
		seed[i] = provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "m"}}}
	}
	a.SetMessages(seed)
	if _, err := a.Compact(context.Background(), AutoCompactKeepTail, nil); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("Compact(keepTail covers all) err = %v; want ErrNothingToCompact", err)
	}
}

// okClient streams a trivial successful turn for every prompt.
type okClient struct{ calls int32 }

func (c *okClient) Name() string { return "ok-fake" }

func (c *okClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "ok-fake", Model: req.Model}
		out <- provider.EventTextDelta{Delta: "ok"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// TestAutoCompactGuardSkipsWhenNothingToCompact reproduces the spurious
// "nothing to compact" trigger: usage is over the threshold (so
// ShouldAutoCompact is true) but the transcript is already entirely
// keep-tail. The CanCompact guard must skip the pre-turn compaction
// entirely — no compact_start/compact_end events, no failure — and let
// the prompt proceed normally.
func TestAutoCompactGuardSkipsWhenNothingToCompact(t *testing.T) {
	client := &okClient{}
	// claude-sonnet-4-5 resolves to a 200k window; prime usage above 85%.
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{})
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000})

	// Tiny transcript: 3 messages, well under keep-tail of 4.
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q2"}}},
	})

	if !a.ShouldAutoCompact(AutoCompactThreshold) {
		t.Fatal("precondition: ShouldAutoCompact should be true with primed usage")
	}
	if a.CanCompact(AutoCompactKeepTail) {
		t.Fatal("precondition: CanCompact should be false for a sub-keep-tail transcript")
	}

	var events []string
	err := a.PromptWithPolicy(context.Background(), "hello", nil, func(ev AgentEvent) {
		events = append(events, ev.Type())
	})
	if err != nil {
		t.Fatalf("PromptWithPolicy returned %v; want nil (guard should skip compaction)", err)
	}
	joined := strings.Join(events, ",")
	if strings.Contains(joined, "compact_start") || strings.Contains(joined, "compact_end") {
		t.Fatalf("guard failed: compaction was attempted on a sub-keep-tail transcript: %v", events)
	}
	// The transcript head must NOT be a synthetic compaction summary.
	if msgs := a.Messages(); len(msgs) > 0 && msgs[0].Meta["compaction"] == "true" {
		t.Fatal("transcript was compacted despite nothing to compact")
	}
}

func TestContextFractionUnknownModelIsZero(t *testing.T) {
	a := NewAgent(nil, "model-that-does-not-exist-xyz", "", Registry{})
	if got := a.ContextFraction(); got != 0 {
		t.Fatalf("ContextFraction = %v, want 0 for unknown model", got)
	}
	if a.ShouldAutoCompact(AutoCompactThreshold) {
		t.Fatal("ShouldAutoCompact true with no usage data")
	}
}
