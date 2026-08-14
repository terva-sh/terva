package workspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// blockingCompactClient parks the compaction's summary request until released,
// so a message can be sent while the compaction is genuinely in flight.
type blockingCompactClient struct {
	calls    int32
	inFlight chan struct{} // closed once the summary request has started
	release  chan struct{} // close to let the summary finish
}

func (c *blockingCompactClient) Name() string { return "blocking-compact" }

func (c *blockingCompactClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	n := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if n == 1 {
			// The compaction's summary request: hold it open.
			close(c.inFlight)
			select {
			case <-c.release:
			case <-ctx.Done():
				out <- provider.EventDone{Stop: provider.StopError, Err: ctx.Err()}
				return
			}
			out <- provider.EventTextDelta{Delta: "summary"}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "summary"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "reply"}},
		}}
	}()
	return out, nil
}

func compactQueueSession(t *testing.T, cl provider.Client) *wsSession {
	t.Helper()
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	s := &wsSession{
		id:    "compact-queue",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: core.NewAgent(cl, "claude-sonnet-4-5", "", core.Registry{}),
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	seed := make([]provider.Message, 0, 8)
	for range 4 {
		seed = append(seed,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		)
	}
	s.agent.SetMessages(seed)
	return s
}

// A message sent while a compaction is running must SURVIVE.
//
// It did not. wsSession decides busy-vs-idle by turnCancel, which tracks turns
// — and a compaction is not a turn, so it never sets it. queue() therefore read
// the session as idle and dispatched the prompt immediately; beginTurn agreed
// and claimed the slot; launchTurn spawned the goroutine and returned nil, so
// queue()'s own "it was busy after all, queue it" fallback never fired. Inside
// the goroutine the AGENT's single-flight — held by the compaction — refused
// with ErrBusy, which launchTurn deliberately excludes from both the error
// banner and the error sidecar as a control-flow rejection.
//
// Net: never sent, never queued, never logged. The user's text was gone, and
// the spurious turn it started cleared the "compacting" note on its way out.
func TestMessageSentDuringCompactionSurvives(t *testing.T) {
	cl := &blockingCompactClient{inFlight: make(chan struct{}), release: make(chan struct{})}
	s := compactQueueSession(t, cl)

	compactDone := make(chan error, 1)
	go func() { compactDone <- s.compact(context.Background()) }()

	select {
	case <-cl.inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never reached the provider")
	}

	// The user types. This is the path Workspace.Queue takes.
	s.queue("keep me")
	// Let the dispatch this provoked actually reach the agent and be refused,
	// rather than racing it against the release below. Without this the
	// goroutine can win, find the compaction already finished, and succeed —
	// which is why the loss is intermittent in practice.
	time.Sleep(200 * time.Millisecond)

	close(cl.release)
	select {
	case err := <-compactDone:
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never finished")
	}

	// The message must be somewhere: still pending, or already delivered into
	// the transcript by a restart. Anywhere but gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s.agent.QueuedMessageCount() > 0 || transcriptHasText(s, "keep me") {
			return // survived
		}
		if time.Now().After(deadline) {
			t.Fatalf("message lost: queue is empty and it never reached the transcript (%d messages)",
				len(s.agent.Messages()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The other half of the report: "the message about compacting disappears".
//
// That symptom is not a rendering bug. Dispatching during a compaction claimed
// the turn slot and ran a whole turn lifecycle — turn start, ErrBusy, done —
// and the client resets its turn UI on that, which clears extNotes, which is
// where the "compacting" note lives. So the note vanished because a turn really
// did start and end underneath it.
//
// The safety net cannot catch this: it hands the text back AFTER the spurious
// turn has already been and gone. Only refusing to dispatch in the first place
// keeps the note on screen — which is why this asserts the message is PENDING
// the instant it is handed over, not merely that it survives.
func TestQueueDuringCompactionStartsNoTurn(t *testing.T) {
	cl := &blockingCompactClient{inFlight: make(chan struct{}), release: make(chan struct{})}
	s := compactQueueSession(t, cl)

	compactDone := make(chan error, 1)
	go func() { compactDone <- s.compact(context.Background()) }()
	select {
	case <-cl.inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never reached the provider")
	}

	s.queue("keep me")

	// Queued, not dispatched. No sleep: this must hold the moment queue returns,
	// because a turn started here is a turn the user watches wipe their screen.
	if got := s.agent.QueuedMessageCount(); got != 1 {
		t.Fatalf("QueuedMessageCount = %d, want 1 — the message was dispatched into the compaction instead of queued", got)
	}
	s.mu.Lock()
	dispatched := s.turnCancel != nil
	s.mu.Unlock()
	if dispatched {
		t.Fatal("a turn was started during compaction; its lifecycle is what clears the compacting note")
	}

	close(cl.release)
	select {
	case err := <-compactDone:
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never finished")
	}

	// And it must not merely survive — it must go out on its own, without the
	// user having to send anything else. A compaction never reaches endTurn, so
	// nothing else would ever shift this queue.
	deadline := time.Now().Add(5 * time.Second)
	for !transcriptHasText(s, "keep me") {
		if time.Now().After(deadline) {
			t.Fatal("queued message never slid in after the compaction finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The safety net, proven on its own: whatever races, a dispatch the AGENT
// refuses must put the user's words back rather than drop them. This is the
// last place that still knows the text — beginTurn already claimed the slot, so
// promptBlocks returned nil and the caller's own fallback is long past.
//
// Exercised directly through prompt() (not queue()) so it holds even if the
// busy-detection above is later changed or bypassed by a new caller.
func TestAgentRefusalRequeuesRatherThanDropping(t *testing.T) {
	cl := &blockingCompactClient{inFlight: make(chan struct{}), release: make(chan struct{})}
	s := compactQueueSession(t, cl)

	compactDone := make(chan error, 1)
	go func() { compactDone <- s.compact(context.Background()) }()
	select {
	case <-cl.inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never reached the provider")
	}

	// Dispatch straight past the busy check, the way a racing caller would.
	if err := s.prompt("do not lose me", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt claimed no slot: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the agent refuse it

	close(cl.release)
	select {
	case <-compactDone:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never finished")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if s.agent.QueuedMessageCount() > 0 || transcriptHasText(s, "do not lose me") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a refused dispatch dropped the user's message")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The other loss in the same path: endTurn classified ErrBusy as a FAILED turn,
// and a failed turn drops the whole pending queue. So one dispatch losing a
// race discarded every follow-up the user had lined up — not just its own text.
// The drop rule is for interrupts; a refusal is not an interrupt.
func TestRefusedDispatchDoesNotDrainTheWholeQueue(t *testing.T) {
	cl := &blockingCompactClient{inFlight: make(chan struct{}), release: make(chan struct{})}
	s := compactQueueSession(t, cl)

	compactDone := make(chan error, 1)
	go func() { compactDone <- s.compact(context.Background()) }()
	select {
	case <-cl.inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never reached the provider")
	}

	// Two follow-ups already lined up behind the compaction.
	s.agent.QueueMessage("first follow-up")
	s.agent.QueueMessage("second follow-up")

	// A racing dispatch the compaction turns away — either queue() sees
	// s.compacting and never dispatches, or it loses the race and the agent
	// refuses with ErrBusy. Both are "turned away"; neither reaches a provider.
	if err := s.prompt("racer", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt claimed no slot: %v", err)
	}
	// A settle window, because the assertion below is a NEGATIVE — that nothing
	// drained the queue — and there is no positive edge to wait for.
	time.Sleep(200 * time.Millisecond)

	// Assert the PREMISE before the conclusion. blockingCompactClient answers
	// every call after the first with a complete turn, so a racer that reached
	// the provider runs a real turn and legitimately shifts the queue behind it.
	// That is a different failure from the one this test exists to catch, and
	// without this check it reported as "a refused dispatch drained the queue" —
	// sending the next reader to the drop rule in endTurn, which would be
	// working correctly. A test whose setup silently stopped holding does not
	// get to name the cause of its own failure.
	if calls := atomic.LoadInt32(&cl.calls); calls != 1 {
		t.Fatalf("the racer reached the provider: client calls = %d, want 1 (the compaction's own). "+
			"It was served, not turned away, so this run says nothing about the drop rule", calls)
	}

	if got := s.agent.QueuedMessageCount(); got < 2 {
		t.Fatalf("queued messages after a refused dispatch = %d, want the 2 follow-ups intact "+
			"(client calls = %d, so the racer was correctly turned away and the queue was drained anyway)",
			got, atomic.LoadInt32(&cl.calls))
	}

	close(cl.release)
	select {
	case <-compactDone:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never finished")
	}
}

func transcriptHasText(s *wsSession, want string) bool {
	for _, m := range s.agent.Messages() {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && tb.Text == want {
				return true
			}
		}
	}
	return false
}
