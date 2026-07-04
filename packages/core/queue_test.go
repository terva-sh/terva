package core

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

type queueFakeClient struct {
	calls int32
}

func (c *queueFakeClient) Name() string { return "queue-fake" }

func (c *queueFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "queue-fake", Model: req.Model}
		switch call {
		case 1:
			out <- provider.EventToolStart{ID: "t1", Name: "echo"}
			out <- provider.EventToolEnd{ID: "t1"}
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{
					provider.TextBlock{Text: "using tool"},
					provider.ToolCallBlock{ID: "t1", Name: "echo", Arguments: json.RawMessage(`{}`)},
				},
			}}
		case 2:
			out <- provider.EventTextDelta{Delta: "saw queued"}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "saw queued"}},
			}}
		default:
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "extra"}},
			}}
		}
	}()
	return out, nil
}

// blockingTool waits until the test has queued a message, then
// returns. This pins the core behaviour: queued user text is delivered
// after the current tool batch finishes and before the next model call.
type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *blockingTool) Name() string            { return "echo" }
func (t *blockingTool) Description() string     { return "echoes" }
func (t *blockingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (t *blockingTool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (ToolResult, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		return ToolResult{Content: []provider.Content{provider.TextBlock{Text: ctx.Err().Error()}}, IsError: true}, ctx.Err()
	case <-t.release:
	}
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "tool ok"}}}, nil
}

func TestQueuedMessageInjectedAfterToolBatchBeforeNextModelCall(t *testing.T) {
	client := &queueFakeClient{}
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	a := NewAgent(client, "fake-model", "system", Registry{"echo": tool})

	var (
		mu    sync.Mutex
		texts []string
	)
	sink := func(ev AgentEvent) {
		switch e := ev.(type) {
		case EvUserMessage:
			mu.Lock()
			texts = append(texts, "user:"+extractText(e.Message))
			mu.Unlock()
		case EvAssistantMessage:
			mu.Lock()
			texts = append(texts, "asst:"+extractText(e.Message))
			mu.Unlock()
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- a.Prompt(context.Background(), "do X", nil, sink)
	}()

	<-tool.started
	if !a.QueueMessage("also do Y") {
		t.Fatal("QueueMessage returned false")
	}
	close(tool.release)

	if err := <-done; err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if got := atomic.LoadInt32(&client.calls); got != 2 {
		t.Fatalf("Stream calls = %d; want 2", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if !queueTestContains(texts, "user:also do Y") {
		t.Fatalf("queued message was not emitted as user message; texts=%v", texts)
	}
	if !queueTestContains(texts, "asst:saw queued") {
		t.Fatalf("second assistant response missing; texts=%v", texts)
	}
}

func queueTestContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestQueueMessageSnapshotPopAndDrain(t *testing.T) {
	a := NewAgent(nil, "fake", "", Registry{})
	if a.QueueMessage("   ") {
		t.Fatal("blank queue message accepted")
	}
	a.QueueMessage("one")
	a.QueueMessage("two")
	if got := a.PendingQueuedMessages(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("PendingQueuedMessages = %v; want [one two]", got)
	}
	if text, ok := a.PopQueuedMessage(); !ok || text != "two" {
		t.Fatalf("PopQueuedMessage = %q,%v; want two,true", text, ok)
	}
	if got := a.DrainQueuedMessages(); len(got) != 1 || got[0] != "one" {
		t.Fatalf("DrainQueuedMessages = %v; want [one]", got)
	}
	if got := a.QueuedMessageCount(); got != 0 {
		t.Fatalf("QueuedMessageCount = %d; want 0", got)
	}
}

func TestSetQueuedMessages(t *testing.T) {
	a := NewAgent(nil, "fake", "", Registry{})
	a.QueueMessage("one")
	a.QueueMessage("two")
	// Replace wholesale (edit/cancel), trimming blanks and dropping empties.
	a.SetQueuedMessages([]string{" edited ", "", "   ", "kept"})
	if got := a.PendingQueuedMessages(); len(got) != 2 || got[0] != "edited" || got[1] != "kept" {
		t.Fatalf("PendingQueuedMessages = %v; want [edited kept]", got)
	}
	// An empty slice clears the queue.
	a.SetQueuedMessages(nil)
	if got := a.QueuedMessageCount(); got != 0 {
		t.Fatalf("QueuedMessageCount = %d; want 0", got)
	}
}

func TestQueueFrontOps(t *testing.T) {
	a := NewAgent(nil, "fake", "", Registry{})
	if a.RequeueFront("  ") {
		t.Fatal("blank requeue accepted")
	}
	a.QueueMessage("later")
	a.RequeueFront("first")
	if got := a.PendingQueuedMessages(); len(got) != 2 || got[0] != "first" || got[1] != "later" {
		t.Fatalf("PendingQueuedMessages = %v; want [first later]", got)
	}
	if text, ok := a.ShiftQueuedMessage(); !ok || text != "first" {
		t.Fatalf("ShiftQueuedMessage = %q,%v; want first,true", text, ok)
	}
	if text, ok := a.ShiftQueuedMessage(); !ok || text != "later" {
		t.Fatalf("ShiftQueuedMessage = %q,%v; want later,true", text, ok)
	}
	if _, ok := a.ShiftQueuedMessage(); ok {
		t.Fatal("shift from empty queue reported ok")
	}
}
