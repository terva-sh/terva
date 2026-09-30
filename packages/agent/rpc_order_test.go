package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// rpcOrderClient records the user text each model call answers. Its first
// call waits for release and then fails, so the prompts behind it queue
// behind a failed turn.
type rpcOrderClient struct {
	mu      sync.Mutex
	seen    []string
	started chan struct{}
	release chan struct{}
}

func (*rpcOrderClient) Name() string { return "order" }

func (c *rpcOrderClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	last := ""
	if n := len(req.Messages); n > 0 {
		for _, b := range req.Messages[n-1].Content {
			if tb, ok := b.(provider.TextBlock); ok {
				last += tb.Text
			}
		}
	}
	c.mu.Lock()
	c.seen = append(c.seen, last)
	first := len(c.seen) == 1
	c.mu.Unlock()
	if first {
		close(c.started)
		<-c.release
		return nil, errors.New("the first turn fails")
	}
	return rpcEchoClient{}.Stream(ctx, req)
}

// Prompts that arrive while a turn runs each run afterwards, in the order
// they arrived, and the first of them runs although the turn before it failed.
func TestRPCRunsQueuedPromptsInArrivalOrder(t *testing.T) {
	run := startRPCQuestionRun(t, nil)
	defer run.close(t)
	client := &rpcOrderClient{started: make(chan struct{}), release: make(chan struct{})}
	run.server.agent = coretest.NewAgent(client, "fake-model", "system", core.Registry{})

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "p0", "type": "prompt", "message": "p0"})
	<-client.started
	const queued = 24
	want := []string{"p0"}
	for i := 1; i <= queued; i++ {
		msg := fmt.Sprintf("p%d", i)
		want = append(want, msg)
		sendRPCQuestionCommand(t, run.in, map[string]any{"id": msg, "type": "prompt", "message": msg})
	}
	close(client.release)
	if frame := nextRPCFrameUntil(t, run.out, "error"); frame["error"] == nil {
		t.Fatalf("the first turn's error frame = %#v", frame)
	}
	for range queued + 1 {
		_ = nextRPCFrameUntil(t, run.out, "done")
	}
	client.mu.Lock()
	seen := slices.Clone(client.seen)
	client.mu.Unlock()
	if !slices.Equal(seen, want) {
		t.Fatalf("the model answered the prompts in the order %v, want %v", seen, want)
	}
}

// A turn that leaves starts only the next turn in line.
func TestTheRPCTurnQueueStartsTurnsInTheOrderTheyJoined(t *testing.T) {
	var q rpcTurnQueue
	first := q.join()
	rest := []<-chan struct{}{q.join(), q.join(), q.join()}
	open := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	if !open(first) {
		t.Fatal("the first turn did not start at once")
	}
	for i := range rest {
		for j, ch := range rest {
			if got, want := open(ch), j < i; got != want {
				t.Fatalf("after %d leaves, turn %d started = %v, want %v", i, j+1, got, want)
			}
		}
		q.leave()
	}
	q.leave()
	if !open(q.join()) {
		t.Fatal("a turn that joined an empty line did not start at once")
	}
}

// A prompt takes its place in line before dispatch returns, on the read loop,
// so the next command read cannot pass it.
func TestAnRPCPromptJoinsTheLineBeforeDispatchReturns(t *testing.T) {
	s := &rpcServer{ctx: context.Background(), out: &rpcSyncWriter{}}
	s.agent = coretest.NewAgent(rpcEchoClient{}, "fake-model", "system", core.Registry{})
	busy := s.turns.join()
	<-busy
	for i := 1; i <= 3; i++ {
		s.dispatch("prompt", fmt.Sprintf("p%d", i), []byte(fmt.Sprintf(`{"message":"p%d"}`, i)))
		s.turns.mu.Lock()
		n := len(s.turns.waiting)
		s.turns.mu.Unlock()
		if n != i {
			t.Fatalf("after dispatch %d, %d prompts wait in line, want %d", i, n, i)
		}
	}
	s.turns.leave()
	s.inFlight.Wait()
}
