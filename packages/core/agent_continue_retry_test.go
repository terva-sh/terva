package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// prefillRetryFakeClient advertises prefill continuation and fails the FIRST
// attempt with a transient, content-less error (an overloaded 5xx — exactly what
// Anthropic returns), then continues cleanly. It records each request so the test
// can assert the retry still presented the assistant prefill as the last message.
type prefillRetryFakeClient struct {
	cont     string
	calls    int32
	lastReqs []provider.Request
}

func (c *prefillRetryFakeClient) Name() string { return "prefill-retry-fake" }

func (c *prefillRetryFakeClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{ContinuesAssistantPrefill: true}
}

func (c *prefillRetryFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.lastReqs = append(c.lastReqs, req)
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "prefill-retry-fake", Model: req.Model}
		if call == 1 {
			// Transient error with NO message — the common overloaded case, where
			// oneTurn keeps nothing (keep=false) and the trailing assistant prefill
			// is left as the last message.
			out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError("anthropic", "overloaded_error: Overloaded", true)}
			return
		}
		out <- provider.EventTextDelta{Delta: c.cont}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.cont}},
		}}
	}()
	return out, nil
}

// TestContinueSurvivesTransientRetry is the regression for the release-review
// blocker: a transient provider error during a ContinueAssistant turn must NOT
// drop the assistant message being continued. Before the fix, runLoop's
// dropLastAssistantMessage deleted the prefill target on the failed attempt, so
// the retry built from a transcript no longer ending in an assistant — the merge
// then failed (append instead), the continuation was lost, and ConsumeContinueResult
// returned nothing.
func TestContinueSurvivesTransientRetry(t *testing.T) {
	client := &prefillRetryFakeClient{cont: " and vanished into the trees."}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})

	if err := a.ContinueAssistant(context.Background(), nil); err != nil {
		t.Fatalf("ContinueAssistant: %v", err)
	}

	// It took two attempts (transient, then success).
	if got := atomic.LoadInt32(&client.calls); got != 2 {
		t.Fatalf("Stream calls = %d; want 2 (one transient, one success)", got)
	}
	// BOTH requests must have ended in the assistant prefill — the retry included,
	// which is only true if the target survived the failed attempt.
	for i, req := range client.lastReqs {
		rm := req.Messages
		if len(rm) == 0 || rm[len(rm)-1].Role != provider.RoleAssistant {
			t.Fatalf("request %d does not end in the assistant prefill: %+v", i, rm)
		}
	}

	// The transcript grew IN PLACE — still two messages, the last one extended
	// (not dropped-and-regenerated).
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("message count = %d; want 2 (merge, not append after retry)", len(msgs))
	}
	if got, want := extractText(msgs[1]), "The knight rode on, and vanished into the trees."; got != want {
		t.Fatalf("merged text = %q; want %q", got, want)
	}

	// The merge is stashed for the caller to persist, at the right index.
	idx, merged, ok := a.ConsumeContinueResult()
	if !ok || idx != 1 {
		t.Fatalf("ConsumeContinueResult = (%d, _, %v); want (1, _, true) — the continuation was lost", idx, ok)
	}
	if got := extractText(merged); got != "The knight rode on, and vanished into the trees." {
		t.Errorf("stashed merged text = %q", got)
	}
}
