package ctrlclient

// The client is tested against the REAL server loop (ctrlproto.ServeConn)
// over an in-memory pipe: handshake, command round-trips through the
// WorkspaceService adapter, wire error-code preservation, event fan-out, and
// the reconnect discipline (pending calls rejected, subscription channels
// closed, resubscribe works on the next connection).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// pipeConn is one end of an in-memory FrameConn pair. Closing either end
// severs the link for both, like a real socket.
type pipeConn struct {
	in   <-chan ctrlproto.Frame
	out  chan<- ctrlproto.Frame
	done chan struct{} // shared by both ends
	once *sync.Once
}

func newPipe() (client, server ctrlproto.FrameConn) {
	a2b := make(chan ctrlproto.Frame, 64)
	b2a := make(chan ctrlproto.Frame, 64)
	done := make(chan struct{})
	once := &sync.Once{}
	return &pipeConn{in: b2a, out: a2b, done: done, once: once},
		&pipeConn{in: a2b, out: b2a, done: done, once: once}
}

func (p *pipeConn) ReadFrame(ctx context.Context) (ctrlproto.Frame, error) {
	select {
	case f := <-p.in:
		return f, nil
	case <-p.done:
		return ctrlproto.Frame{}, errors.New("pipe closed")
	case <-ctx.Done():
		return ctrlproto.Frame{}, ctx.Err()
	}
}

func (p *pipeConn) WriteFrame(ctx context.Context, f ctrlproto.Frame) error {
	select {
	case p.out <- f:
		return nil
	case <-p.done:
		return errors.New("pipe closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pipeConn) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// fakeSvc implements the handful of WorkspaceService methods the tests
// exercise; the embedded nil interface panics loudly on anything unexpected.
type fakeSvc struct {
	ctrlproto.WorkspaceService

	mu      sync.Mutex
	prompts []string
	events  chan ctrlproto.Event
}

func (f *fakeSvc) Prompt(_ context.Context, sess string, p ctrlproto.PromptParams) error {
	text := p.Text
	if text == "busy" {
		return ctrlproto.Errorf(ctrlproto.CodeBusy, "a turn is already running")
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, sess+":"+text)
	f.mu.Unlock()
	return nil
}

func (f *fakeSvc) Sessions(context.Context) ([]ctrlproto.SessionInfo, error) {
	return []ctrlproto.SessionInfo{{ID: "s1", Model: "m1", Current: true}}, nil
}

func (f *fakeSvc) Usage(context.Context, string) (core.WireUsage, error) {
	return core.WireUsage{Input: 42, Output: 7}, nil
}

func (f *fakeSvc) Subscribe(ctx context.Context, _ string) (<-chan ctrlproto.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = make(chan ctrlproto.Event, 16)
	return f.events, nil
}

// harness runs a Client against ServeConn-backed fake service over pipes; each
// (re)dial mints a fresh pipe + server loop, like a listener accepting again.
type harness struct {
	svc     *fakeSvc
	client  *Client
	mu      sync.Mutex
	serverC []ctrlproto.FrameConn // per-connection server ends, oldest first
	hellos  []ctrlproto.Hello
	drops   int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{svc: &fakeSvc{}}
	c, err := New(Options{
		Backoff: 10 * time.Millisecond,
		Dial: func(ctx context.Context) (ctrlproto.FrameConn, error) {
			cc, sc := newPipe()
			h.mu.Lock()
			h.serverC = append(h.serverC, sc)
			h.mu.Unlock()
			go func() {
				_, _ = ctrlproto.ServeConn(ctx, sc, h.svc, ctrlproto.ServerHello("test-daemon", "9.9.9"))
				_ = sc.Close()
			}()
			return cc, nil
		},
		OnConnect: func(server ctrlproto.Hello) {
			h.mu.Lock()
			h.hellos = append(h.hellos, server)
			h.mu.Unlock()
		},
		OnDisconnect: func(error) {
			h.mu.Lock()
			h.drops++
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.client = c
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = c.Close() })
	go func() { _ = c.Run(ctx) }()
	waitFor(t, "connect", c.Connected)
	return h
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) connects() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.hellos)
}

// dropConn severs the current connection server-side.
func (h *harness) dropConn() {
	h.mu.Lock()
	sc := h.serverC[len(h.serverC)-1]
	h.mu.Unlock()
	_ = sc.Close()
}

func TestHandshakeAndCalls(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	svc := h.client.Service()

	if hello, ok := h.client.ServerHello(); !ok || hello.Version != "9.9.9" || hello.Agent != "test-daemon" {
		t.Fatalf("server hello = %+v ok=%v, want test-daemon 9.9.9", hello, ok)
	}

	if err := svc.Prompt(ctx, "s1", ctrlproto.PromptParams{Text: "hello"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	list, err := svc.Sessions(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "s1" || !list[0].Current {
		t.Fatalf("Sessions = %+v, %v", list, err)
	}
	u, err := svc.Usage(ctx, "s1")
	if err != nil || u.Input != 42 || u.Output != 7 {
		t.Fatalf("Usage = %+v, %v", u, err)
	}
}

func TestWireErrorCodePreserved(t *testing.T) {
	h := newHarness(t)
	err := h.client.Service().Prompt(context.Background(), "s1", ctrlproto.PromptParams{Text: "busy"})
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBusy {
		t.Fatalf("err = %v, want *ctrlproto.Error with code busy", err)
	}
}

func TestSubscribeDeliversEvents(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := h.client.Subscribe(ctx, "s1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	h.svc.mu.Lock()
	evs := h.svc.events
	h.svc.mu.Unlock()
	evs <- ctrlproto.NoticeEvent("info", "", "hi")

	select {
	case ev := <-ch:
		if ev.Notice == nil || ev.Notice.Text != "hi" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event delivered")
	}
}

func TestDisconnectRejectsAndResyncs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	ch, err := h.client.Subscribe(subCtx, "s1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	h.dropConn()

	// The subscription channel closes — the resync signal.
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected closed subscription channel after drop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel not closed after drop")
	}

	// The client reconnects on its own and a fresh subscribe works.
	waitFor(t, "reconnect", func() bool { return h.connects() >= 2 && h.client.Connected() })
	ch2, err := h.client.Subscribe(subCtx, "s1")
	if err != nil {
		t.Fatalf("re-Subscribe: %v", err)
	}
	h.svc.mu.Lock()
	evs := h.svc.events
	h.svc.mu.Unlock()
	evs <- ctrlproto.NoticeEvent("info", "", "again")
	select {
	case ev := <-ch2:
		if ev.Notice == nil || ev.Notice.Text != "again" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event after resubscribe")
	}

	h.mu.Lock()
	drops := h.drops
	h.mu.Unlock()
	if drops < 1 {
		t.Fatalf("OnDisconnect fired %d times, want >= 1", drops)
	}
}

func TestCallFailsFastWhenDown(t *testing.T) {
	h := newHarness(t)
	h.dropConn()
	waitFor(t, "down", func() bool { return !h.client.Connected() })
	// Immediately after the drop (before the backoff reconnect lands) a
	// call must fail fast, not hang.
	err := h.client.Service().Cancel(context.Background(), "s1")
	if !errors.Is(err, ErrNotConnected) && !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err = %v, want ErrNotConnected/ErrDisconnected", err)
	}
}

func TestCloseStopsReconnecting(t *testing.T) {
	h := newHarness(t)
	_ = h.client.Close()
	waitFor(t, "down after close", func() bool { return !h.client.Connected() })
	before := h.connects()
	time.Sleep(50 * time.Millisecond) // several backoff periods
	if after := h.connects(); after != before {
		t.Fatalf("client reconnected after Close (%d -> %d)", before, after)
	}
	if err := h.client.Service().Cancel(context.Background(), "s1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// A subscriber cancelled while the read loop is fanning out must not panic the
// dispatcher. dispatchEvent copies the subscriber pointers out of the registry
// and then sends without c.mu, while Subscribe's ctx AfterFunc closes the very
// same channel from another goroutine — so send has to be ORDERED against
// close, not merely idempotent. Before the per-subscription lock this panicked
// with "send on closed channel", killing the whole terva attach process on an
// ordinary /sessions switch during a live turn.
func TestDispatchEventRacesSubscriptionClose(t *testing.T) {
	for range 300 {
		c := &Client{subs: map[string][]*subscription{}}
		const n = 16
		list := make([]*subscription, n)
		for i := range list {
			// Buffer 1 with 2 events in flight also exercises the overflow
			// close path racing the cancellation close.
			list[i] = &subscription{sess: "s", ch: make(chan ctrlproto.Event, 1)}
		}
		// The registry gets its own backing array: removeSub compacts in
		// place, and the loop below must not be ranging over the same one.
		c.subs["s"] = append([]*subscription(nil), list...)

		var wg sync.WaitGroup
		wg.Add(1 + n)
		go func() {
			defer wg.Done()
			c.dispatchEvent("s", ctrlproto.Event{})
			c.dispatchEvent("s", ctrlproto.Event{})
		}()
		for _, s := range list {
			go func(s *subscription) {
				defer wg.Done()
				c.removeSub(s)
				s.close() // what context.AfterFunc does on subscription ctx end
			}(s)
		}
		wg.Wait()
	}
}
