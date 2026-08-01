package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// Compaction is the one provider call that must not give up easily, because
// giving up leaves the session in the state that triggered it: a transcript too
// big for its window, and now with no way down. The turn loop has had a
// transient-failure ladder from the start; the summarizer had none, so a single
// "Our servers are currently overloaded. Please try again later." — the error
// the wire protocol itself flags transient — killed the compaction outright.
//
// These pin the ladder, and pin the budget SHARING that keeps it from turning
// one overloaded provider into eight transcript-sized requests.

// overloaded is the in-stream error frame that started all this. It arrives
// AFTER the response headers, which is why provider-level retry (which covers
// only pre-header failures) never saw it.
func overloaded() []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 0}},
		provider.EventDone{
			Stop: provider.StopError,
			Err: provider.NewAPIError("openai-codex",
				"Our servers are currently overloaded. Please try again later.", true),
		},
	}
}

// rejected is a NON-transient failure: it must not consume the retry budget,
// and it must not be retried at all.
func rejected() []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventDone{
			Stop: provider.StopError,
			Err:  provider.NewAPIError("openai-codex", "http 400: invalid_request_error", false),
		},
	}
}

// retryingAgent is cacheAwareAgent with a backoff short enough to test.
func retryingAgent(t *testing.T, client provider.Client) *Agent {
	t.Helper()
	a := cacheAwareAgent(t, client)
	a.RetryBaseDelay = time.Millisecond
	return a
}

// The headline case: the provider blips once, the compaction rides it out. The
// warm summarizer must retry IN PLACE rather than fall through — falling
// through answers a transient overload with a full-price cold re-read of the
// whole transcript, sent to the provider that just said it was overloaded.
func TestCompactionRetriesATransientOverload(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0: // the turn
			return saidText("hi", 50), nil
		case 1: // warm compaction, blipped
			return overloaded(), nil
		default: // warm compaction, retried
			return saidText("## Goal\nship it", 100), nil
		}
	}}
	a := retryingAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Compact returned %v; a transient overload must be retried, not surfaced", err)
	}

	calls := client.calls()
	if len(calls) != 3 {
		t.Fatalf("client saw %d requests; want 3 (turn, blipped warm, retried warm)", len(calls))
	}
	// The retry is the SAME strategy. If the second compaction request carries a
	// flattened transcript instead of the cached message list, the ladder fell
	// through to the cold path and paid full price for a blip.
	if calls[2].EphemeralContext == "" || len(calls[2].Messages) != len(calls[1].Messages) {
		t.Errorf("the retry is not a warm request — it fell through to the cold summarizer instead of retrying in place")
	}
	if res.Strategy != CompactWarm {
		t.Errorf("Strategy = %q; want %q — the warm path succeeded, on its second try", res.Strategy, CompactWarm)
	}
	if res.FallbackReason != "" {
		t.Errorf("FallbackReason = %q; want empty — nothing fell back", res.FallbackReason)
	}

	// Every attempt's spend, abandoned ones included: a.cost already folded the
	// blipped attempt into the cumulative total, and SessionUsageDetail
	// subtracts this row back out of the last-turn delta. Report only the
	// winning attempt and the difference lands on the context gauge as phantom
	// turn spend.
	if res.Usage.InputTokens != 200 {
		t.Errorf("CompactResult.Usage.InputTokens = %d; want 200 (100 blipped + 100 succeeded)", res.Usage.InputTokens)
	}
}

// A non-transient failure must not be retried, and must not spend the budget
// the cold fallback is going to need. This is the case the shared budget could
// most easily get wrong: warm fails for an ordinary reason, and the cold path
// arrives already broke.
func TestNonTransientWarmFailureLeavesTheRetryBudgetIntact(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0: // the turn
			return saidText("hi", 50), nil
		case 1: // warm: the model used a tool instead of summarizing
			return calledATool(100), nil
		case 2, 3: // cold, overloaded twice
			return overloaded(), nil
		default: // cold, third time lucky
			return saidText("## Goal\nship it", 100), nil
		}
	}}
	a := retryingAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Compact returned %v; the cold path had budget left and should have ridden it out", err)
	}
	if got := len(client.calls()); got != 5 {
		t.Fatalf("client saw %d requests; want 5 (turn, warm tool_use, cold ×3) — a warm failure that spent no retries must hand the cold path a full budget", got)
	}
	if res.Strategy != CompactWarmFellBack {
		t.Errorf("Strategy = %q; want %q", res.Strategy, CompactWarmFellBack)
	}
	if res.FallbackReason != "tool_use" {
		t.Errorf("FallbackReason = %q; want %q — the fallback was the tool answer, not the overloads that followed it", res.FallbackReason, "tool_use")
	}
}

// The other half of the shared budget: a warm attempt that DID burn it on
// overloads must not hand the cold path a fresh one. Without sharing, one
// struggling provider gets hit with MaxRetries+1 warm requests and then
// MaxRetries+1 cold ones, each the size of the whole transcript.
func TestTheRetryBudgetIsSharedAcrossBothSummarizers(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return overloaded(), nil
	}}
	a := retryingAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := a.Compact(context.Background(), 0, nil)
	if err == nil {
		t.Fatal("Compact succeeded against a provider that never recovered")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("Compact error = %v; want the provider's own overload text", err)
	}
	// 1 turn + 4 warm (first + MaxRetries) + 1 cold (first, budget spent).
	want := 1 + (1 + a.MaxRetries) + 1
	if got := len(client.calls()); got != want {
		t.Fatalf("client saw %d requests; want %d — the cold fallback must inherit the SPENT budget, not a fresh one", got, want)
	}
}

// A rejection is terminal on the first try. The needle this guards: the ladder
// must classify by the error's own Transient flag, never by prose, or an
// oversize rejection starts costing four transcript-sized requests instead of
// falling straight through to the path that might actually fit.
func TestCompactionDoesNotRetryANonTransientRejection(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return rejected(), nil
	}}
	a := retryingAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err == nil {
		t.Fatal("Compact succeeded on a 400")
	}
	if got := len(client.calls()); got != 3 {
		t.Errorf("client saw %d requests; want 3 (turn, warm, cold) — a 400 must not be retried", got)
	}
}

// Esc during the backoff has to end the compaction now, not after the timer.
// The ladder sleeps between attempts, and a cancel that only takes effect when
// the timer fires is a UI that ignores the user for seconds at a time.
func TestCompactionBackoffIsCancellable(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return overloaded(), nil
	}}
	a := cacheAwareAgent(t, client)
	a.RetryBaseDelay = time.Hour // only a cancel can get us out of this

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := a.Compact(ctx, 0, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Compact error = %v; want the cancellation, not the provider error it was waiting out", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Compact did not return after cancel; the backoff sleep is not watching ctx")
	}
}

// A provider that was down for the whole ladder is a fact about the provider,
// not about the cache-aware summarizer. Bucketing it as a plain "error" reads in
// the A/B as the warm path failing, and the warm path is what the A/B exists to
// judge.
func TestExhaustedOverloadIsNotBucketedAsAWarmFailure(t *testing.T) {
	// The budget is DERIVED, not spelled. "Down for the whole ladder" is the
	// premise of this test, and a literal turns it into "down for exactly four
	// requests" — which silently becomes a different test the moment MaxRetries
	// moves. It did move (3 → 6), and the warm path then recovered on its fifth
	// try, so there was no fallback left to classify and the assertion failed
	// pointing at the classifier rather than at the fixture.
	var a *Agent
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch {
		case n == 0:
			return saidText("hi", 50), nil
		case n <= 1+a.MaxRetries: // warm: first attempt + every retry, all overloaded
			return overloaded(), nil
		default: // cold gets through
			return saidText("## Goal\nship it", 100), nil
		}
	}}
	a = retryingAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Compact returned %v", err)
	}
	if res.FallbackReason != "provider_unavailable" {
		t.Errorf("FallbackReason = %q; want %q", res.FallbackReason, "provider_unavailable")
	}
}
