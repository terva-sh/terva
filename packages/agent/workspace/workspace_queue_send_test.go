package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// sendClient answers its first two requests with a tool call and every later
// one with text. It records the user texts that each request carried, which
// is what the model saw at that call.
type sendClient struct {
	mu   sync.Mutex
	seen [][]string
}

func (c *sendClient) Name() string { return "send-fake" }

func (c *sendClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	var users []string
	for _, m := range req.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if tb, ok := b.(provider.TextBlock); ok {
				users = append(users, tb.Text)
			}
		}
	}
	c.mu.Lock()
	c.seen = append(c.seen, users)
	n := len(c.seen)
	c.mu.Unlock()

	out := make(chan provider.Event, 1)
	go func() {
		defer close(out)
		if n <= 2 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{
					ID: "T" + string(rune('0'+n)), Name: "step", Arguments: json.RawMessage(`{}`),
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

func (c *sendClient) requests() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.seen...)
}

// firstCallGate blocks its first call until the turn is cancelled, and
// returns at once on every later call. The first turn therefore stays inside
// a tool until something stops it.
//
// With a release channel the first call ignores the cancel and waits for the
// channel. The turn then stays alive after a send has cancelled it, which is
// the window that a second send or an interrupt can arrive in.
type firstCallGate struct {
	entered chan struct{}
	release chan struct{}
	calls   *atomic.Int32
}

func (firstCallGate) Name() string            { return "step" }
func (firstCallGate) Description() string     { return "advances the turn" }
func (firstCallGate) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (g firstCallGate) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if g.calls.Add(1) == 1 {
		close(g.entered)
		if g.release != nil {
			<-g.release
		}
		<-ctx.Done()
		return core.ToolResult{}, ctx.Err()
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// sendTestSession starts a turn that waits inside a tool, with three notes
// queued behind it.
func sendTestSession(t *testing.T) (*wsSession, *sendClient) {
	t.Helper()
	return sendTestSessionWith(t, nil)
}

// sendTestSessionWith is sendTestSession with a first tool call that outlives
// a cancel until release closes.
func sendTestSessionWith(t *testing.T, release chan struct{}) (*wsSession, *sendClient) {
	t.Helper()
	client := &sendClient{}
	gate := firstCallGate{entered: make(chan struct{}), release: release, calls: &atomic.Int32{}}
	s := newTurnTestSession(t, client)
	s.agent.SetTools(core.Registry{"step": gate})
	s.agent.AddQueueDrainedObserver(s.queueDrained)
	if err := s.prompt("the first prompt", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-gate.entered
	for _, note := range []string{"note a", "note b", "note c"} {
		s.queue(note)
	}
	return s, client
}

// waitSettled waits until the provider has had want requests and the session
// is idle. The request count comes first: the session is idle for an instant
// between the turn that a send stops and the turn that it starts, so idle
// alone can be that instant.
func waitSettled(t *testing.T, s *wsSession, c *sendClient, want int) [][]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := c.requests(); len(reqs) >= want && !s.busy() {
			return reqs
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the provider got %d requests, want %d: %v", len(c.requests()), want, c.requests())
	return nil
}

// afterFirstPrompt reports the texts that follow the first prompt in a request.
func afterFirstPrompt(t *testing.T, users []string) []string {
	t.Helper()
	if len(users) == 0 || users[0] != "the first prompt" {
		t.Fatalf("the request lost the first prompt: %v", users)
	}
	return users[1:]
}

// One entry goes now. The turn that waited in a tool stops, the next turn
// starts on that entry, and its first model call sees that entry alone. The
// two that were not sent reach the model at the next boundary.
func TestSendQueuedInterruptsTheTurnWithOneEntry(t *testing.T) {
	s, client := sendTestSession(t)

	if err := s.sendQueued(ctrlproto.QueueSendParams{Index: 1, Text: "note b"}); err != nil {
		t.Fatalf("sendQueued: %v", err)
	}
	reqs := waitSettled(t, s, client, 3)
	if len(reqs) != 3 {
		t.Fatalf("the provider got %d requests, want 3: %v", len(reqs), reqs)
	}
	if got := afterFirstPrompt(t, reqs[1]); !reflect.DeepEqual(got, []string{"note b"}) {
		t.Fatalf("the new turn's first request carried %v, want the sent entry alone", got)
	}
	if got := afterFirstPrompt(t, reqs[2]); !reflect.DeepEqual(got, []string{"note b", "note a", "note c"}) {
		t.Fatalf("the next request carried %v, want the held entries behind the sent one", got)
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("%d entries still wait", n)
	}
	// The first prompt had no reply when the turn stopped. A person's
	// interrupt would have withdrawn it, and a send must not.
	want := [][2]string{{"the first prompt", ""}, {"note b", ""}, {"note a", ""}, {"note c", ""}}
	if got := userSources(s.snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot user messages = %v, want %v", got, want)
	}
}

// The whole queue goes now, in its order, and all of it reaches the first
// model call of the new turn.
func TestSendQueuedAllDeliversTheWholeQueue(t *testing.T) {
	s, client := sendTestSession(t)

	if err := s.sendQueued(ctrlproto.QueueSendParams{All: true}); err != nil {
		t.Fatalf("sendQueued: %v", err)
	}
	reqs := waitSettled(t, s, client, 3)
	if got := afterFirstPrompt(t, reqs[1]); !reflect.DeepEqual(got, []string{"note a", "note b", "note c"}) {
		t.Fatalf("the new turn's first request carried %v, want the whole queue", got)
	}
}

// The control for both tests above: a plain interrupt drops the queue and
// starts no turn. Without it, a harness that restarted on any cancel would
// pass them.
func TestAPlainInterruptStillDropsTheQueue(t *testing.T) {
	s, client := sendTestSession(t)

	s.interruptTurn()
	waitIdle(t, s)

	if reqs := client.requests(); len(reqs) != 1 {
		t.Fatalf("the provider got %d requests after an interrupt, want 1: %v", len(reqs), reqs)
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("%d entries survived the interrupt", n)
	}
}

// The queue changed since the client looked: the entry at that index holds
// another text. The session refuses, keeps the queue, and keeps the turn.
func TestSendQueuedRefusesAStaleEntry(t *testing.T) {
	s, _ := sendTestSession(t)
	defer func() {
		s.interruptTurn()
		waitIdle(t, s)
	}()

	for _, p := range []ctrlproto.QueueSendParams{
		{Index: 0, Text: "note b"},
		{Index: 3},
		{Index: -1},
	} {
		err := s.sendQueued(p)
		var ce *ctrlproto.Error
		if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeConflict {
			t.Fatalf("sendQueued(%+v) = %v, want a conflict", p, err)
		}
	}
	got := make([]string, 0, 3)
	for _, m := range s.agent.PendingQueuedMessages() {
		got = append(got, m.Text)
	}
	if !reflect.DeepEqual(got, []string{"note a", "note b", "note c"}) {
		t.Fatalf("a refused send changed the queue: %v", got)
	}
	if !s.busy() {
		t.Fatal("a refused send stopped the turn")
	}
}

// An idle session has no turn to stop. The send is then an accepted no-op on
// an empty queue.
func TestSendQueuedOnAnEmptyQueueDoesNothing(t *testing.T) {
	s := newTurnTestSession(t, &sendClient{})
	if err := s.sendQueued(ctrlproto.QueueSendParams{All: true}); err != nil {
		t.Fatalf("sendQueued: %v", err)
	}
	if s.busy() {
		t.Fatal("a send on an empty queue started a turn")
	}
}

// Two sends of one entry each meet before the stopped turn has ended. Both
// entries reach the first model call of the new turn, in the order of the
// sends, and the entry that nobody picked still waits for the next boundary.
func TestTwoSendsThatMeetHoldTheUnpickedEntry(t *testing.T) {
	release := make(chan struct{})
	s, client := sendTestSessionWith(t, release)

	if err := s.sendQueued(ctrlproto.QueueSendParams{Index: 1, Text: "note b"}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	// The queue is now [note a, note c], and the turn is still inside its tool.
	if err := s.sendQueued(ctrlproto.QueueSendParams{Index: 1, Text: "note c"}); err != nil {
		t.Fatalf("second send: %v", err)
	}
	close(release)

	reqs := waitSettled(t, s, client, 3)
	if got := afterFirstPrompt(t, reqs[1]); !reflect.DeepEqual(got, []string{"note b", "note c"}) {
		t.Fatalf("the new turn's first request carried %v, want the two sent entries alone", got)
	}
	if got := afterFirstPrompt(t, reqs[2]); !reflect.DeepEqual(got, []string{"note b", "note c", "note a"}) {
		t.Fatalf("the next request carried %v, want the unpicked entry last", got)
	}
}

// A person sends an entry and then presses stop before the turn has ended.
// The stop wins: no turn starts, and the queue goes as it does on every
// interrupt.
func TestAnInterruptAfterASendStartsNoTurn(t *testing.T) {
	release := make(chan struct{})
	s, client := sendTestSessionWith(t, release)

	if err := s.sendQueued(ctrlproto.QueueSendParams{Index: 1, Text: "note b"}); err != nil {
		t.Fatalf("sendQueued: %v", err)
	}
	s.interruptTurn()
	// The turn's cause is still the send's, so the stop is on the session,
	// for the report to a talkoot.
	s.mu.Lock()
	stopped := s.stoppedAfterSend
	s.mu.Unlock()
	if !stopped {
		t.Fatal("the interrupt after a send was not recorded as a person's stop")
	}
	close(release)
	waitIdle(t, s)
	// A restart would come after the idle instant, so give one the time to
	// show. The control is TestSendQueuedInterruptsTheTurnWithOneEntry, where
	// the same send without the interrupt reaches three requests.
	time.Sleep(100 * time.Millisecond)
	waitIdle(t, s)

	if reqs := client.requests(); len(reqs) != 1 {
		t.Fatalf("the provider got %d requests after the interrupt, want 1: %v", len(reqs), reqs)
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("%d entries survived the interrupt", n)
	}
	// No sent entry reached the conversation.
	if got := userSources(s.snapshot()); !reflect.DeepEqual(got, [][2]string{{"the first prompt", ""}}) {
		t.Fatalf("snapshot user messages = %v, want the first prompt alone", got)
	}
	s.mu.Lock()
	stale := s.stoppedAfterSend
	s.mu.Unlock()
	if stale {
		t.Fatal("the stop outlived its turn")
	}
}

// A send that arrives during a compaction waits for it, and an interrupt in
// that time stops no turn, so the send stays. The end of the compaction
// starts the turn on the sent entry, with the hold: the two entries that
// nobody picked are not in its first request.
func TestASendDuringACompactionSurvivesAnInterrupt(t *testing.T) {
	client := &sendClient{}
	s := newTurnTestSession(t, client)
	s.agent.SetTools(core.Registry{"step": firstCallGate{entered: make(chan struct{}), calls: func() *atomic.Int32 {
		// Every call returns at once: the gate is for a first call that no
		// test here makes.
		var n atomic.Int32
		n.Store(1)
		return &n
	}()}})
	s.agent.AddQueueDrainedObserver(s.queueDrained)

	s.mu.Lock()
	s.compacting = true
	s.mu.Unlock()
	for _, note := range []string{"note a", "note b", "note c"} {
		s.queue(note)
	}

	if err := s.sendQueued(ctrlproto.QueueSendParams{Index: 1, Text: "note b"}); err != nil {
		t.Fatalf("sendQueued: %v", err)
	}
	s.interruptTurn()
	if got := len(client.requests()); got != 0 {
		t.Fatalf("a turn started during the compaction: %d requests", got)
	}
	s.endCompacting()

	reqs := waitSettled(t, s, client, 3)
	if !reflect.DeepEqual(reqs[0], []string{"note b"}) {
		t.Fatalf("the turn's first request carried %v, want the sent entry alone", reqs[0])
	}
	if !reflect.DeepEqual(reqs[1], []string{"note b", "note a", "note c"}) {
		t.Fatalf("the next request carried %v, want the held entries behind the sent one", reqs[1])
	}
}
