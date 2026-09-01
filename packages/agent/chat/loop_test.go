package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeConnector is an in-memory Connector: tests push inbound
// messages and inspect outbound sends. This is the fake the
// connector plan promised — the loop is tested with no network.
type fakeConnector struct {
	caps    Capabilities
	inbound chan Message

	mu     sync.Mutex
	sent   []Outgoing
	typing int
	stops  int
	// events records typing pulses and stops in arrival order, so a
	// test can assert the stop came LAST.
	events []string
	images []string
	files  []string
}

func newFakeConnector(caps Capabilities) *fakeConnector {
	return &fakeConnector{caps: caps, inbound: make(chan Message, 16)}
}

func (f *fakeConnector) Name() string { return "fake" }

func (f *fakeConnector) Connect(context.Context) (Identity, error) {
	return Identity{ID: "1", Username: "fakebot"}, nil
}

func (f *fakeConnector) Receive(ctx context.Context, handle func(Message)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-f.inbound:
			handle(m)
		}
	}
}

func (f *fakeConnector) Send(_ context.Context, out Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, out)
	return nil
}

func (f *fakeConnector) SendImage(_ context.Context, chatID, path, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, path)
	return nil
}

func (f *fakeConnector) SendFile(_ context.Context, chatID, path, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append(f.files, path)
	return nil
}

func (f *fakeConnector) Typing(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	f.events = append(f.events, "pulse")
	return nil
}

func (f *fakeConnector) StopTyping(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.events = append(f.events, "stop")
	return nil
}

func (f *fakeConnector) Capabilities() Capabilities { return f.caps }

func (f *fakeConnector) sends() []Outgoing {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Outgoing(nil), f.sent...)
}

// waitSends polls until at least n messages were sent or the deadline
// hits.
func (f *fakeConnector) waitSends(t *testing.T, n int) []Outgoing {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := f.sends(); len(s) >= n {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sends; got %v", n, f.sends())
	return nil
}

// scriptedClient is a provider.Client whose turns produce fixed text.
// gate, when non-nil, blocks each turn until released (or the turn
// context dies) so tests can hold the loop busy.
type scriptedClient struct {
	reply string
	err   error
	gate  chan struct{}

	calls      atomic.Int32
	concurrent atomic.Int32
	maxConc    atomic.Int32
}

func (c *scriptedClient) Name() string { return "scripted" }

func (c *scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.calls.Add(1)
	if n := c.concurrent.Add(1); n > c.maxConc.Load() {
		c.maxConc.Store(n)
	}
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		defer c.concurrent.Add(-1)
		if c.gate != nil {
			select {
			case <-c.gate:
			case <-ctx.Done():
				out <- provider.EventDone{Stop: provider.StopAborted, Err: ctx.Err()}
				return
			}
		}
		if c.err != nil {
			out <- provider.EventDone{Stop: provider.StopError, Err: c.err}
			return
		}
		out <- provider.EventTextDelta{Delta: c.reply}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// startLoop runs a Loop against the fake connector and returns it
// with a cancel for cleanup.
func startLoop(t *testing.T, conn *fakeConnector, client provider.Client, pairing Pairing) *Loop {
	t.Helper()
	l := &Loop{
		Connector: conn,
		Agent:     core.NewAgent(client, "fake-model", "sys", core.Registry{}),
		Provider:  "fake",
		CWD:       "/ws",
		Pairing:   pairing,
		Info:      func(string) {},
		Warn:      func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()
	return l
}

func pairedWith(userID string) Pairing { return Pairing{AllowedUserID: userID} }

func msgFrom(user, text string) Message {
	return Message{ChatID: "100", UserID: user, Username: "u" + user, ReplyTo: "1", Text: text}
}

func TestLoopPairingFlow(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	var saved string
	startLoop(t, conn, &scriptedClient{reply: "ok"}, Pairing{
		Save: func(uid string) error { saved = uid; return nil },
	})

	// Unpaired: a plain message is refused with instructions.
	conn.inbound <- msgFrom("7", "hello")
	s := conn.waitSends(t, 1)
	if !strings.Contains(s[0].Text, "isn't paired") {
		t.Fatalf("unpaired reply = %q", s[0].Text)
	}

	// First /start claims it.
	conn.inbound <- msgFrom("7", "/start")
	s = conn.waitSends(t, 2)
	if !strings.Contains(s[1].Text, "paired with @u7") {
		t.Fatalf("pairing ack = %q", s[1].Text)
	}
	if saved != "7" {
		t.Fatalf("pairing persisted %q, want 7", saved)
	}

	// Another user is rejected.
	conn.inbound <- msgFrom("8", "hi")
	s = conn.waitSends(t, 3)
	if !strings.Contains(s[2].Text, "different user") {
		t.Fatalf("other-user reply = %q", s[2].Text)
	}

	// The paired user's message now reaches the agent.
	conn.inbound <- msgFrom("7", "what is 2+2?")
	s = conn.waitSends(t, 4)
	if s[3].Text != "ok" {
		t.Fatalf("agent reply = %q", s[3].Text)
	}
}

func TestLoopChunksLongReply(t *testing.T) {
	conn := newFakeConnector(Capabilities{MaxTextLen: 10})
	startLoop(t, conn, &scriptedClient{reply: "0123456789ABCDEFGHIJ"}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "go")
	s := conn.waitSends(t, 2)
	for i, out := range s {
		if len(out.Text) > 10 {
			t.Errorf("chunk %d exceeds limit: %q", i, out.Text)
		}
	}
	joined := ""
	for _, out := range s {
		joined += out.Text
	}
	if joined != "0123456789ABCDEFGHIJ" {
		t.Errorf("chunks reassemble to %q", joined)
	}
}

func TestLoopQueuesWhileBusy(t *testing.T) {
	gate := make(chan struct{})
	client := &scriptedClient{reply: "done", gate: gate}
	conn := newFakeConnector(Capabilities{})
	startLoop(t, conn, client, pairedWith("7"))

	conn.inbound <- msgFrom("7", "first")
	conn.inbound <- msgFrom("7", "second")

	// Both inbound messages are consumed; only one turn may run.
	deadline := time.Now().Add(2 * time.Second)
	for client.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(gate) // release both turns

	conn.waitSends(t, 2)
	if got := client.maxConc.Load(); got != 1 {
		t.Errorf("max concurrent turns = %d, want 1 (queue must serialize)", got)
	}
	if got := client.calls.Load(); got != 2 {
		t.Errorf("turns run = %d, want 2", got)
	}
}

func TestLoopStopCancelsActiveTurn(t *testing.T) {
	gate := make(chan struct{})
	client := &scriptedClient{reply: "never", gate: gate}
	conn := newFakeConnector(Capabilities{})
	startLoop(t, conn, client, pairedWith("7"))

	conn.inbound <- msgFrom("7", "long task")
	deadline := time.Now().Add(2 * time.Second)
	for client.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	conn.inbound <- msgFrom("7", "stop")
	s := conn.waitSends(t, 1)
	found := false
	for _, out := range s {
		if strings.Contains(out.Text, "cancelled the current turn") {
			found = true
		}
	}
	if !found {
		// The cancel reply may land after the aborted turn's filler.
		s = conn.waitSends(t, 2)
		for _, out := range s {
			if strings.Contains(out.Text, "cancelled the current turn") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no cancellation acknowledgment; sends: %v", s)
	}
}

func TestLoopErrorReply(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	startLoop(t, conn, &scriptedClient{err: errors.New("kaboom")}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "go")
	s := conn.waitSends(t, 1)
	if !strings.Contains(s[0].Text, "error: kaboom") {
		t.Fatalf("error reply = %q", s[0].Text)
	}
}

func TestLoopTypingPulse(t *testing.T) {
	gate := make(chan struct{})
	conn := newFakeConnector(Capabilities{TypingRefresh: 5 * time.Millisecond})
	startLoop(t, conn, &scriptedClient{reply: "ok", gate: gate}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "go")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.mu.Lock()
		n := conn.typing
		conn.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	conn.mu.Lock()
	n := conn.typing
	conn.mu.Unlock()
	if n < 2 {
		t.Errorf("typing pulses = %d, want >= 2", n)
	}
}

// A connector that declared typing_stop gets exactly one stop per
// turn, after the reply, and after its last pulse — never a pulse
// after the stop, which would re-light the indicator it just cleared.
func TestLoopTypingStopFollowsTheLastPulse(t *testing.T) {
	conn := newFakeConnector(Capabilities{TypingRefresh: 5 * time.Millisecond, TypingStop: true})
	startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "go")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.mu.Lock()
		n := conn.stops
		conn.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond) // several refresh intervals: a late pulse would land here
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.stops != 1 {
		t.Fatalf("typing stops = %d, want exactly 1 (events %v)", conn.stops, conn.events)
	}
	if len(conn.sent) != 1 {
		t.Fatalf("sent = %d, want the one reply", len(conn.sent))
	}
	if last := conn.events[len(conn.events)-1]; last != "stop" {
		t.Errorf("typing events = %v, want the stop last", conn.events)
	}
}

// Without the declaration the loop never calls StopTyping, even though
// the fake implements it: an undeclared connector would read the frame
// as one more start.
func TestLoopTypingNoStopWithoutTheFeature(t *testing.T) {
	conn := newFakeConnector(Capabilities{TypingRefresh: 5 * time.Millisecond})
	startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "go")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.mu.Lock()
		n := len(conn.sent)
		conn.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.stops != 0 {
		t.Errorf("typing stops = %d, want 0 without typing_stop declared", conn.stops)
	}
}

func TestLoopStatusCommand(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	conn.inbound <- msgFrom("7", "/status")
	s := conn.waitSends(t, 1)
	if !strings.Contains(s[0].Text, "fake-model") || !strings.Contains(s[0].Text, "state: idle") {
		t.Fatalf("status reply = %q", s[0].Text)
	}
}
