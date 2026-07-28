package workspace

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// gatedStepTool is the tool the scripted turn calls, so the loop runs a second
// step and therefore crosses a mid-turn safe boundary.
//
// It parks inside Execute until the test releases it. That park is the whole
// point: it holds the turn open at a genuinely mid-turn moment so the test can
// queue a message and know the loop has not yet reached the boundary that
// consumes it. Signalling merely that the tool STARTED is not enough — a tool
// that returns immediately lets the loop drain an empty queue and move on
// before the test gets a word in.
type gatedStepTool struct {
	entered chan struct{}
	release chan struct{}
}

func (gatedStepTool) Name() string            { return "step" }
func (gatedStepTool) Description() string     { return "advances the turn" }
func (gatedStepTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t gatedStepTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	select {
	case t.entered <- struct{}{}:
	default:
	}
	<-t.release
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// twoStepClient scripts a genuine agentic turn: a tool call, then a final
// answer. The tool call is what forces a second loop iteration, and the top of
// that iteration is the safe boundary where queued messages are consumed.
type twoStepClient struct {
	mu    sync.Mutex
	calls int
}

func (c *twoStepClient) Name() string { return "two-step-fake" }

func (c *twoStepClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if n == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{
					ID: "T1", Name: "step", Arguments: json.RawMessage(`{}`),
				}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// A message queued during a long agentic turn is consumed by the loop at its
// next safe boundary — between steps, on the turn goroutine, with no host call
// involved. The daemon has to say so.
//
// It used to say nothing. The queue emptied, the message landed in the
// transcript, and every client that mirrors the queue went on rendering it as
// still-pending: a phantom "sliding in" row sitting under the very message it
// had already become. It could not double-send — the agent's queue is
// authoritative and was already empty — but the next real queue_updated
// REPLACED the phantom rather than appending to it, so queueing a second
// message looked like it had overwritten the first.
//
// Only a multi-step turn reaches this, which is why the simple case always
// looked right: a single-step turn ends first, and endTurn's shift broadcasts
// on the way out.
func TestMidTurnDrainBroadcastsTheQueue(t *testing.T) {
	tool := gatedStepTool{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, &twoStepClient{})
	s.agent.SetTools(core.Registry{"step": tool})
	s.agent.AddQueueDrainedObserver(func([]string) { s.broadcastQueue() })

	queues := collectQueueEvents(t, s)

	if err := s.prompt("run the tool", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-tool.entered // parked inside the tool: the turn is live and mid-step

	// Queue while the turn is held open. The daemon announces this — that half
	// always worked, because the host performed the mutation itself.
	s.queue("a note")
	if q, ok := queues.await(func(q []string) bool { return len(q) == 1 && q[0] == "a note" }); !ok {
		t.Fatalf("the queued message was never announced; saw %v", q)
	}

	// Let the tool finish. The loop now enters step 2, and drains the queue on
	// the way in — the mutation nobody asked for.
	close(tool.release)

	if _, ok := queues.await(func(q []string) bool { return len(q) == 0 }); !ok {
		t.Fatal("the loop consumed the queued message and the daemon never said so — " +
			"every client keeps rendering it as pending, under the message it already became")
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Errorf("agent queue holds %d messages after the drain, want 0", n)
	}
}

// queueLog accumulates every queue_updated the session broadcasts. A live turn
// floods the hub with conversation events and a lossy subscriber drops under
// backpressure, so the events are drained continuously rather than looked for
// only when the test happens to want one.
type queueLog struct {
	mu   sync.Mutex
	seen [][]string
	ping chan struct{}
}

func collectQueueEvents(t *testing.T, s *wsSession) *queueLog {
	t.Helper()
	l := &queueLog{ping: make(chan struct{}, 64)}
	sub := s.hub.add(nil, false)
	go func() {
		for ev := range sub {
			if ev.Type != ctrlproto.EventQueueUpdated {
				continue
			}
			l.mu.Lock()
			l.seen = append(l.seen, ev.Queued)
			l.mu.Unlock()
			select {
			case l.ping <- struct{}{}:
			default:
			}
		}
	}()
	return l
}

// await waits for a queue_updated matching want, and reports what it saw.
func (l *queueLog) await(want func([]string) bool) ([][]string, bool) {
	deadline := time.After(3 * time.Second)
	for {
		l.mu.Lock()
		for _, q := range l.seen {
			if want(q) {
				l.mu.Unlock()
				return nil, true
			}
		}
		l.mu.Unlock()
		select {
		case <-l.ping:
		case <-deadline:
			l.mu.Lock()
			seen := append([][]string(nil), l.seen...)
			l.mu.Unlock()
			return seen, false
		}
	}
}
