package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// overloadClient fails the first failFor attempts with the exact error a real
// codex overload produces — an in-stream error frame carrying no HTTP status,
// which is the path that reaches the prose classifier — then succeeds.
type overloadClient struct {
	calls   int32
	failFor int32
}

func (c *overloadClient) Name() string { return "openai-codex" }

func (c *overloadClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	n := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "openai-codex", Model: req.Model}
		if n <= c.failFor {
			out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError(
				"openai-codex", "Our servers are currently overloaded. Please try again later.", true)}
			return
		}
		out <- provider.EventTextDelta{Delta: "recovered"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "recovered"}},
		}}
	}()
	return out, nil
}

// The defect this whole change exists to fix: the backoff ran, but nothing
// said so. A ~20s stall followed by the provider's raw sentence is exactly
// what one immediate failure looks like, so a working retry was reported as a
// missing one.
func TestTransientRetryIsAnnouncedOnTheSink(t *testing.T) {
	a := NewAgent(&overloadClient{failFor: 2}, "m", "sys", Registry{})
	a.RetryBaseDelay = time.Millisecond // keep the test honest AND fast

	var retries []EvRetry
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if r, ok := ev.(EvRetry); ok {
			retries = append(retries, r)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(retries) != 2 {
		t.Fatalf("got %d retry events, want 2 (one per failed attempt)", len(retries))
	}
	for i, r := range retries {
		if r.Attempt != i+1 {
			t.Errorf("event %d: Attempt = %d, want %d (1-based, the attempt that failed)", i, r.Attempt, i+1)
		}
		if r.Max != a.MaxRetries {
			t.Errorf("event %d: Max = %d, want %d", i, r.Max, a.MaxRetries)
		}
		if r.Provider != "openai-codex" {
			t.Errorf("event %d: Provider = %q, want openai-codex", i, r.Provider)
		}
		if !strings.Contains(r.Err, "overloaded") {
			t.Errorf("event %d: Err = %q, want the provider's own message", i, r.Err)
		}
		if r.Delay <= 0 {
			t.Errorf("event %d: Delay = %v, want the wait it is about to take", i, r.Delay)
		}
	}
	// The message must NOT repeat the provider name — that rides its own field,
	// and a renderer composing both would say it twice.
	if strings.Contains(retries[0].Err, "openai-codex") {
		t.Errorf("Err = %q, want the bare message without the provider prefix", retries[0].Err)
	}
}

// A turn that recovers must not announce a retry it never took, and one that
// succeeds first time must say nothing at all — otherwise the note becomes
// noise and stops meaning anything.
func TestNoRetryEventWhenNothingFails(t *testing.T) {
	a := NewAgent(&overloadClient{failFor: 0}, "m", "sys", Registry{})
	seen := 0
	if err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if _, ok := ev.(EvRetry); ok {
			seen++
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if seen != 0 {
		t.Fatalf("got %d retry events on a clean turn, want 0", seen)
	}
}

// When the budget runs out the error itself must say what was tried. Three
// surfaces read this string — the red banner, the session error sidecar, and
// the rescue dialog's reason — and all three used to show the provider's bare
// sentence. A sidecar row reading only "servers are overloaded" is precisely
// what made a working backoff look absent when the failure was investigated
// after the fact.
func TestExhaustedRetriesSayHowHardItTried(t *testing.T) {
	a := NewAgent(&overloadClient{failFor: 99}, "m", "sys", Registry{})
	a.RetryBaseDelay = time.Millisecond

	err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {})
	if err == nil {
		t.Fatal("Prompt succeeded; want the exhausted-retry error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gave up after") {
		t.Errorf("error = %q, want it to say the attempts were made", msg)
	}
	if !strings.Contains(msg, "overloaded") {
		t.Errorf("error = %q, want the provider's own message preserved", msg)
	}
	// The wrap must not break typed classification: the rescue dialog and the
	// retry policy both reach for the ProviderError underneath.
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatal("wrapping lost the *provider.ProviderError")
	}
	if !pe.Transient {
		t.Error("underlying error should still read as transient")
	}
	// And it must not accidentally look like a class it isn't. The old needle
	// list retried "prompt is too long: 208500 tokens" because "500" matched;
	// an appended attempt count is the same hazard if worded carelessly.
	if ok, reason := ClassifyRecoverable(err); !ok {
		t.Errorf("ClassifyRecoverable = false; an overload is recoverable (reason %q)", reason)
	} else if strings.Contains(reason, "rate limited") || strings.Contains(reason, "authentication") {
		t.Errorf("suffix pushed the error into the wrong class: %q", reason)
	}
}

// A single failure that is never retried must NOT claim attempts it didn't
// make — the count has to be trustworthy or it is worse than absent.
func TestSingleAttemptErrorSaysNothingAboutRetries(t *testing.T) {
	a := NewAgent(&permanentClient{}, "m", "sys", Registry{})
	err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {})
	if err == nil {
		t.Fatal("want the permanent error")
	}
	if strings.Contains(err.Error(), "gave up after") {
		t.Errorf("error = %q, want no attempt count on a non-retried failure", err.Error())
	}
}

// permanentClient fails once, permanently — the classifier must not retry it.
type permanentClient struct{}

func (c *permanentClient) Name() string { return "openai-codex" }

func (c *permanentClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "openai-codex", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopError,
			Err: provider.NewAPIError("openai-codex", "invalid request", false)}
	}()
	return out, nil
}

// The event has to survive the wire, or every remote client (the TUI talks to
// its own daemon over ctrlproto) is back to the silent stall.
func TestRetryEventSurvivesTheWire(t *testing.T) {
	ev := EvRetry{Provider: "openai-codex", Attempt: 5, Max: 6,
		Delay: 32 * time.Second, Err: "Our servers are currently overloaded."}
	w := EventToWire(ev)
	if w.Type != "retry" {
		t.Fatalf("Type = %q, want retry", w.Type)
	}
	if w.Retry == nil {
		t.Fatal("Retry payload dropped on the wire")
	}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back WireEvent
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Retry == nil {
		t.Fatal("Retry payload dropped in round-trip")
	}
	if back.Retry.DelayMS != 32000 {
		t.Errorf("DelayMS = %d, want 32000", back.Retry.DelayMS)
	}
	if back.Retry.Attempt != 5 || back.Retry.Max != 6 {
		t.Errorf("attempt/max = %d/%d, want 5/6", back.Retry.Attempt, back.Retry.Max)
	}
}
