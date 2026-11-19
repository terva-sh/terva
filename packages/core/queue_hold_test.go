package core

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// holdFakeClient answers its first request with a tool call and every later
// one with text. It records the user texts that each request carried.
type holdFakeClient struct {
	mu   sync.Mutex
	seen [][]string
}

func (c *holdFakeClient) Name() string { return "hold-fake" }

func (c *holdFakeClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	var users []string
	for _, m := range req.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		if text := extractText(m); text != "" {
			users = append(users, text)
		}
	}
	c.mu.Lock()
	c.seen = append(c.seen, users)
	call := len(c.seen)
	c.mu.Unlock()

	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "hold-fake", Model: req.Model}
		if call == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: "echo", Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

type instantTool struct{}

func (instantTool) Name() string            { return "echo" }
func (instantTool) Description() string     { return "echoes" }
func (instantTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (instantTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "tool ok"}}}, nil
}

// requestsWithQueue queues one text, runs a prompt with the given hold, and
// returns the user texts of each provider request.
func requestsWithQueue(t *testing.T, hold bool) [][]string {
	t.Helper()
	client := &holdFakeClient{}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": instantTool{}})
	if !a.QueueMessage("a note that waits") {
		t.Fatal("QueueMessage returned false")
	}
	if err := a.PromptWithPolicyExtra(context.Background(), "the picked one", nil, UserMessageExtras{HoldQueue: hold}, nil); err != nil {
		t.Fatalf("prompt returned %v", err)
	}
	if n := a.QueuedMessageCount(); n != 0 {
		t.Fatalf("%d texts still wait after the turn; want 0", n)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.seen
}

func TestHoldQueueKeepsTheQueueOutOfTheFirstRequest(t *testing.T) {
	got := requestsWithQueue(t, true)
	want := [][]string{
		{"the picked one"},
		{"the picked one", "a note that waits"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests carried %v; want %v", got, want)
	}
}

// The control: with no hold, the first boundary takes the queued text, so the
// test above measures the hold and not the fake.
func TestWithoutHoldQueueTheFirstRequestCarriesTheQueue(t *testing.T) {
	got := requestsWithQueue(t, false)
	if len(got) == 0 || !reflect.DeepEqual(got[0], []string{"the picked one", "a note that waits"}) {
		t.Fatalf("requests carried %v; want the queued text in the first one", got)
	}
}

// A hold belongs to the prompt that set it. The next prompt must drain at its
// first boundary again.
func TestHoldQueueDoesNotReachTheNextPrompt(t *testing.T) {
	client := &holdFakeClient{}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": instantTool{}})
	if err := a.PromptWithPolicyExtra(context.Background(), "first", nil, UserMessageExtras{HoldQueue: true}, nil); err != nil {
		t.Fatalf("first prompt returned %v", err)
	}
	a.QueueMessage("a later note")
	if err := a.Prompt(context.Background(), "second", nil, nil); err != nil {
		t.Fatalf("second prompt returned %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	// The first request of the second prompt is the third request overall.
	third := client.seen[2]
	if !reflect.DeepEqual(third, []string{"first", "second", "a later note"}) {
		t.Fatalf("the second prompt's first request carried %v", third)
	}
}

func TestTakeQueuedMessage(t *testing.T) {
	a := newTestAgent(&holdFakeClient{}, "fake-model", "system", nil)
	a.QueueMessage("one")
	a.QueueMessageFrom("swarm:recap", "two")
	a.QueueMessage("three")

	if _, ok := a.TakeQueuedMessage(1, "one", nil); ok {
		t.Fatal("took an entry whose text the caller did not see there")
	}
	if _, ok := a.TakeQueuedMessage(3, "", nil); ok {
		t.Fatal("took an entry past the end")
	}
	if _, ok := a.TakeQueuedMessage(-1, "", nil); ok {
		t.Fatal("took an entry before the start")
	}
	if got := queuedTexts(a.PendingQueuedMessages()); !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("a refused take changed the queue: %v", got)
	}

	m, ok := a.TakeQueuedMessage(1, " two ", nil)
	if !ok || m.Text != "two" || m.Source != "swarm:recap" {
		t.Fatalf("took %+v, %v; want the second entry with its source", m, ok)
	}
	if got := queuedTexts(a.PendingQueuedMessages()); !reflect.DeepEqual(got, []string{"one", "three"}) {
		t.Fatalf("queue after the take: %v", got)
	}
}

// A host takes one entry and cancels the turn inside the take. The stopped
// loop reaches its next boundary and must take nothing: the entries that stay
// are not for the turn that was stopped.
func TestACancelInsideTheTakeKeepsTheQueueFromTheStoppedLoop(t *testing.T) {
	client := &holdFakeClient{}
	// The tool ignores the cancel, so the loop reaches the boundary after it
	// with a cancelled context. That is the window the take must close.
	tool := &deafTool{started: make(chan struct{}), release: make(chan struct{})}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": tool})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Prompt(ctx, "first", nil, nil) }()
	<-tool.started
	a.QueueMessage("picked")
	a.QueueMessage("stays")
	if _, ok := a.TakeQueuedMessage(0, "picked", cancel); !ok {
		t.Fatal("the take was refused")
	}
	if ctx.Err() == nil {
		t.Fatal("the take did not run the cancel")
	}
	close(tool.release)
	<-done

	if got := queuedTexts(a.PendingQueuedMessages()); !reflect.DeepEqual(got, []string{"stays"}) {
		t.Fatalf("queue after the stopped loop: %v", got)
	}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && extractText(m) == "stays" {
			t.Fatal("the stopped turn took the entry that stayed")
		}
	}
}

// The control: the same loop with no cancel takes the entry that stays, so
// the test above measures the cancel and not the tool.
func TestATakeWithNoCancelLeavesTheLoopItsQueue(t *testing.T) {
	client := &holdFakeClient{}
	tool := &deafTool{started: make(chan struct{}), release: make(chan struct{})}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": tool})

	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), "first", nil, nil) }()
	<-tool.started
	a.QueueMessage("picked")
	a.QueueMessage("stays")
	if _, ok := a.TakeQueuedMessage(0, "picked", nil); !ok {
		t.Fatal("the take was refused")
	}
	close(tool.release)
	if err := <-done; err != nil {
		t.Fatalf("prompt returned %v", err)
	}
	if n := a.QueuedMessageCount(); n != 0 {
		t.Fatalf("%d entries wait; the loop should have taken the one that stayed", n)
	}
}

// deafTool waits for release and does not hear a cancel.
type deafTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *deafTool) Name() string            { return "echo" }
func (t *deafTool) Description() string     { return "echoes" }
func (t *deafTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *deafTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	close(t.started)
	<-t.release
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "tool ok"}}}, nil
}

// Under a hold, the first boundary still takes the counted lead from the
// front of the queue and leaves the rest.
func TestQueueLeadTakesTheFrontOfAHeldQueue(t *testing.T) {
	client := &holdFakeClient{}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": instantTool{}})
	a.QueueMessage("lead")
	a.QueueMessage("held")
	if err := a.PromptWithPolicyExtra(context.Background(), "picked", nil, UserMessageExtras{HoldQueue: true, QueueLead: 1}, nil); err != nil {
		t.Fatalf("prompt returned %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	want := [][]string{{"picked", "lead"}, {"picked", "lead", "held"}}
	if !reflect.DeepEqual(client.seen, want) {
		t.Fatalf("requests carried %v; want %v", client.seen, want)
	}
}

// The rule holds for every cancel, and not for a send's alone: a loop whose
// context is cancelled takes no queued text. The text stays queued for the
// host, which decides what a stopped turn's queue becomes.
func TestACancelledLoopTakesNoQueuedText(t *testing.T) {
	client := &holdFakeClient{}
	tool := &deafTool{started: make(chan struct{}), release: make(chan struct{})}
	a := newTestAgent(client, "fake-model", "system", Registry{"echo": tool})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Prompt(ctx, "first", nil, nil) }()
	<-tool.started
	a.QueueMessage("typed during the turn")
	cancel()
	close(tool.release)
	<-done

	if got := queuedTexts(a.PendingQueuedMessages()); !reflect.DeepEqual(got, []string{"typed during the turn"}) {
		t.Fatalf("queue after the cancelled loop: %v", got)
	}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && extractText(m) == "typed during the turn" {
			t.Fatal("the cancelled turn took a queued text that it cannot answer")
		}
	}
}
