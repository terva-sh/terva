package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// costRaceFakeClient streams a usage row then completes. It blocks on
// gate so the test can read Cost() concurrently while the turn is mid
// flight, exercising the cost-tracker lock under -race.
type costRaceFakeClient struct {
	gate chan struct{}
}

func (c *costRaceFakeClient) Name() string { return "cost-race-fake" }

func (c *costRaceFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "cost-race-fake", Model: req.Model}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}}
		if c.gate != nil {
			<-c.gate
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// TestCostReadDuringTurnIsRaceFree spins Cost()/LastTurnUsage() reads
// from goroutines while a turn folds usage in from its stream loop. Run
// with -race this fails if cost access is not synchronized.
func TestCostReadDuringTurnIsRaceFree(t *testing.T) {
	gate := make(chan struct{})
	client := &costRaceFakeClient{gate: gate}
	a := NewAgent(client, "fake-model", "system", Registry{})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = a.Cost()
					_ = a.LastTurnUsage()
				}
			}
		}()
	}

	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), "hello", nil, nil) }()

	// Let the readers race against the usage fold, then release the turn.
	time.Sleep(5 * time.Millisecond)
	close(gate)

	if err := <-done; err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	close(stop)
	wg.Wait()

	if got := a.Cost(); got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Fatalf("cumulative usage = %+v; want input 10 output 5", got)
	}
}

// appendCountingRetryClient fails the first turn with a retryable error
// after streaming a partial message, then succeeds on the second.
type appendCountingRetryClient struct {
	calls int32
}

func (c *appendCountingRetryClient) Name() string { return "append-counting-retry" }

func (c *appendCountingRetryClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "append-counting-retry", Model: req.Model}
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

// TestRetriedTurnFiresAppendOnceForFinalMessage asserts that the
// durable-append hook fires exactly once after a retried turn, and only
// for the final (recovered) assistant message — never for the dropped
// partial. This guards against the phantom partial-assistant
// persistence bug where the JSONL kept both attempts.
func TestRetriedTurnFiresAppendOnceForFinalMessage(t *testing.T) {
	client := &appendCountingRetryClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	var appended []provider.Message
	a.OnMessageAppended = func(m provider.Message) {
		appended = append(appended, m)
	}

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}

	// Expect exactly two appends: the user prompt and the final
	// assistant message. The dropped partial must NOT have been
	// persisted.
	var assistantAppends []provider.Message
	for _, m := range appended {
		if m.Role == provider.RoleAssistant {
			assistantAppends = append(assistantAppends, m)
		}
	}
	if len(assistantAppends) != 1 {
		t.Fatalf("assistant OnMessageAppended fired %d times; want exactly 1 (final only)", len(assistantAppends))
	}
	if got := extractText(assistantAppends[0]); got != "recovered" {
		t.Fatalf("appended assistant text = %q; want recovered (not the partial)", got)
	}
}

// blockingFakeClient holds the turn open until released so a second
// concurrent Prompt observes the single-flight guard.
type blockingFakeClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingFakeClient) Name() string { return "blocking-fake" }

func (c *blockingFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "blocking-fake", Model: req.Model}
		select {
		case c.started <- struct{}{}:
		default:
		}
		<-c.release
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// TestConcurrentPromptReturnsErrBusy proves the single-flight guard: a
// second Prompt issued while the first is in flight returns ErrBusy
// instead of interleaving transcript appends.
func TestConcurrentPromptReturnsErrBusy(t *testing.T) {
	client := &blockingFakeClient{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	a := NewAgent(client, "fake-model", "system", Registry{})

	first := make(chan error, 1)
	go func() { first <- a.Prompt(context.Background(), "first", nil, nil) }()

	// Wait until the first turn is actually running.
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first Prompt never started")
	}

	if err := a.Prompt(context.Background(), "second", nil, nil); err != ErrBusy {
		t.Fatalf("concurrent Prompt error = %v; want ErrBusy", err)
	}

	close(client.release)
	if err := <-first; err != nil {
		t.Fatalf("first Prompt returned %v", err)
	}

	// After the first run completes the guard is cleared and a new
	// Prompt succeeds.
	if err := a.Prompt(context.Background(), "third", nil, nil); err == ErrBusy {
		t.Fatal("Prompt after completion returned ErrBusy; guard not cleared")
	}
}
