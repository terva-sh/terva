package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

type retryFakeClient struct {
	calls int32
}

func (c *retryFakeClient) Name() string { return "retry-fake" }

func (c *retryFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "retry-fake", Model: req.Model}
		if call == 1 {
			out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError("anthropic", "overloaded_error: Overloaded", true)}
			return
		}
		out <- provider.EventTextDelta{Delta: "ok"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func TestAgentRetriesOverloadedStreamError(t *testing.T) {
	client := &retryFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	var turnErrs []string
	err := a.Prompt(context.Background(), "hello", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvTurnEnd); ok && e.Err != nil {
			turnErrs = append(turnErrs, e.Err.Error())
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if got := atomic.LoadInt32(&client.calls); got != 2 {
		t.Fatalf("Stream calls = %d; want 2", got)
	}
	if len(turnErrs) != 1 || !strings.Contains(turnErrs[0], "overloaded_error") {
		t.Fatalf("turn errors = %v; want one overloaded error before retry", turnErrs)
	}
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("message count = %d; want user + final assistant", len(msgs))
	}
	if got := extractText(msgs[1]); got != "ok" {
		t.Fatalf("final assistant text = %q; want ok", got)
	}
}

type partialRetryFakeClient struct {
	calls int32
}

func (c *partialRetryFakeClient) Name() string { return "partial-retry-fake" }

func (c *partialRetryFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "partial-retry-fake", Model: req.Model}
		if call == 1 {
			out <- provider.EventTextDelta{Delta: "partial"}
			out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewHTTPError("retry-fake", 503, "", "service unavailable"), Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "partial"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "recovered"}},
		}}
	}()
	return out, nil
}

func TestAgentDropsPartialAssistantBeforeRetry(t *testing.T) {
	client := &partialRetryFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("message count = %d; want user + recovered assistant", len(msgs))
	}
	if got := extractText(msgs[1]); got != "recovered" {
		t.Fatalf("final assistant text = %q; want recovered", got)
	}
}

// captureClient records the last Request it received so tests can
// assert what the agent put on the wire.
type captureClient struct {
	lastReq provider.Request
}

func (c *captureClient) Name() string { return "capture" }

func (c *captureClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.lastReq = req
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "capture", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

func TestAgentPropagatesMaxTokens(t *testing.T) {
	client := &captureClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.MaxTokens = 64000

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.lastReq.MaxTokens != 64000 {
		t.Fatalf("request MaxTokens = %d; want 64000 (Agent.MaxTokens not propagated)", client.lastReq.MaxTokens)
	}
}

func TestAgentPropagatesTemperature(t *testing.T) {
	client := &captureClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	temp := float32(0)
	a.Temperature = &temp

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.lastReq.Temperature == nil || *client.lastReq.Temperature != temp {
		t.Fatalf("request Temperature = %v; want %v", client.lastReq.Temperature, temp)
	}
}

// untypedErrClient returns a prose-only error that LOOKS retryable
// under the old substring needles ("http 500"). The typed contract
// must NOT retry it: untyped errors retry only when they classify as
// transport failures by type.
type untypedErrClient struct {
	calls int32
}

func (c *untypedErrClient) Name() string { return "untyped-fake" }

func (c *untypedErrClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventDone{Stop: provider.StopError, Err: errors.New("prompt is too long: 208500 tokens (http 500 lookalike)")}
	}()
	return out, nil
}

// TestAgentDoesNotRetryUntypedProseError pins the contract change: a
// plain error whose MESSAGE resembles a retryable failure is not
// retried. (Under the old needle list, "500" in a token count was
// enough to burn three retries.)
func TestAgentDoesNotRetryUntypedProseError(t *testing.T) {
	client := &untypedErrClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err == nil {
		t.Fatalf("Prompt should surface the error")
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("Stream calls = %d; want 1 (no retry for untyped prose)", got)
	}
}

// TestAgentDoesNotRetryPermanentProviderError: typed, but not
// transient (e.g. 400 validation) — no retry.
type permanentErrClient struct {
	calls int32
}

func (c *permanentErrClient) Name() string { return "perm-fake" }

func (c *permanentErrClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewHTTPError("perm-fake", 400, "", "invalid request")}
	}()
	return out, nil
}

func TestAgentDoesNotRetryPermanentProviderError(t *testing.T) {
	client := &permanentErrClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err == nil {
		t.Fatalf("Prompt should surface the error")
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("Stream calls = %d; want 1 (400 is permanent)", got)
	}
}

// TestRetryDelayHonorsRetryAfter: a server-stated Retry-After wins over
// exponential backoff, capped at MaxRetryDelay — the same ceiling terva's own
// backoff climbs to. One bound, not two: a header asking for a wait terva would
// take on its own judgement is not hostile, and the cap exists to stop the
// pathological case, not to second-guess a cooperative one.
func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	a := NewAgent(&retryFakeClient{}, "fake-model", "system", Registry{})
	a.RetryBaseDelay = 2 * time.Second

	with := provider.NewHTTPError("x", 429, "7", "slow down")
	if got := a.retryDelay(0, with); got != 7*time.Second {
		t.Errorf("retryDelay with Retry-After = %v, want 7s", got)
	}
	huge := provider.NewHTTPError("x", 429, "600", "slow down")
	if got := a.retryDelay(0, huge); got != MaxRetryDelay {
		t.Errorf("retryDelay with huge Retry-After = %v, want the %v cap", got, MaxRetryDelay)
	}
	without := provider.NewHTTPError("x", 503, "", "unavailable")
	if got := a.retryDelay(1, without); got != 4*time.Second {
		t.Errorf("retryDelay fallback = %v, want base*2 = 4s", got)
	}
}

// TestRetryBackoffCurve pins the whole default curve, because the numbers ARE
// the decision: 14s of total patience was too short for the provider overloads
// it mostly meets, and the fix is only meaningful if the tail actually reaches
// a minute. Doubling early (cheap blips recover in seconds) and flat at the
// ceiling late (an overloaded backend gets waited out).
func TestRetryBackoffCurve(t *testing.T) {
	a := NewAgent(&retryFakeClient{}, "fake-model", "system", Registry{})
	err := provider.NewAPIError("openai-codex", "overloaded", true)

	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, MaxRetryDelay}
	if a.MaxRetries != len(want) {
		t.Fatalf("MaxRetries = %d, want %d (the curve below has one entry per retry)", a.MaxRetries, len(want))
	}
	var total time.Duration
	for i, w := range want {
		got := a.retryDelay(i, err)
		if got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i, got, w)
		}
		total += got
	}
	if total < 2*time.Minute {
		t.Errorf("total patience = %v, want at least 2m", total)
	}
	// The last wait is the one the operator asked for: a full minute before
	// terva gives up on a backend that said "try again later".
	if want[len(want)-1] != 60*time.Second {
		t.Errorf("final backoff = %v, want 60s", want[len(want)-1])
	}
	// A host that sets an absurd MaxRetries must not wrap the shift into a
	// zero-length "wait".
	if got := a.retryDelay(64, err); got != MaxRetryDelay {
		t.Errorf("retryDelay(64) = %v, want the %v cap (shift guard)", got, MaxRetryDelay)
	}
}

// TestAgentDoesNotRetryQuotaExhaustion: 429s that are really billing
// exhaustion must not burn retry attempts.
type quotaErrClient struct {
	calls int32
}

func (c *quotaErrClient) Name() string { return "quota-fake" }

func (c *quotaErrClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewHTTPError("quota-fake", 429, "", "monthly usage limit reached")}
	}()
	return out, nil
}

func TestAgentDoesNotRetryQuotaExhaustion(t *testing.T) {
	client := &quotaErrClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err == nil {
		t.Fatalf("Prompt should surface the error")
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("Stream calls = %d; want 1 (quota exhaustion is not transient)", got)
	}
}

// TestAgentStillRefusesQuotaExhaustionThatSaysTryAgainLater guards the widened
// prose list: "try again later" now means retry, but a usage-limit refusal that
// happens to end with the same courtesy must still fail fast rather than burn
// attempts against a wall it cannot get past.
func TestAgentStillRefusesQuotaExhaustionThatSaysTryAgainLater(t *testing.T) {
	client := &politeQuotaClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err == nil {
		t.Fatal("Prompt should surface the quota error")
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("Stream calls = %d; want 1 (quota exhaustion is not retryable, however politely phrased)", got)
	}
}

type politeQuotaClient struct{ calls int32 }

func (c *politeQuotaClient) Name() string { return "quota-fake" }

func (c *politeQuotaClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventDone{Stop: provider.StopError,
			Err: provider.NewHTTPError("quota-fake", 429, "", "You have hit your monthly usage limit. Please try again later.")}
	}()
	return out, nil
}
