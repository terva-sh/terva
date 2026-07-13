package chat

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// fakeHost records the Bridge's callbacks into the TUI.
type fakeHost struct {
	mu        sync.Mutex
	submitted []string
	cancels   int
	notifies  []string
}

func (h *fakeHost) SubmitOrQueue(prompt string, images []provider.ImageBlock) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.submitted = append(h.submitted, prompt)
}

func (h *fakeHost) CancelTurn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels++
}

func (h *fakeHost) Status() string { return "STATUS-LINE" }

func (h *fakeHost) Notify(level, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifies = append(h.notifies, level+": "+message)
}

func (h *fakeHost) waitSubmitted(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		s := append([]string(nil), h.submitted...)
		h.mu.Unlock()
		if len(s) >= n {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d submissions", n)
	return nil
}

func startBridge(t *testing.T, conn *fakeConnector, host *fakeHost, pairing Pairing) *Bridge {
	t.Helper()
	b := &Bridge{Connector: conn, Host: host, Pairing: pairing}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}

func TestBridgeForwardsPromptToHost(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	startBridge(t, conn, host, pairedWith("7"))

	conn.inbound <- msgFrom("7", "do the thing")
	got := host.waitSubmitted(t, 1)
	if got[0] != "do the thing" {
		t.Fatalf("submitted %q", got[0])
	}
}

func TestBridgeStatusAndStop(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	startBridge(t, conn, host, pairedWith("7"))

	conn.inbound <- msgFrom("7", "/status")
	s := conn.waitSends(t, 1)
	if s[0].Text != "STATUS-LINE" {
		t.Fatalf("status reply = %q", s[0].Text)
	}

	conn.inbound <- msgFrom("7", "/stop")
	s = conn.waitSends(t, 2)
	if !strings.Contains(s[1].Text, "cancelled") {
		t.Fatalf("stop reply = %q", s[1].Text)
	}
	host.mu.Lock()
	cancels := host.cancels
	host.mu.Unlock()
	if cancels != 1 {
		t.Fatalf("CancelTurn calls = %d, want 1", cancels)
	}
}

func TestBridgeMirrorsTUITraffic(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	b := startBridge(t, conn, host, pairedWith("7"))

	// A chat message seeds the reply destination.
	conn.inbound <- msgFrom("7", "from chat")
	host.waitSubmitted(t, 1)

	b.OnUserTyped("typed in tui")
	b.OnAssistantText("assistant reply")

	s := conn.waitSends(t, 2)
	if s[0].Text != "you: typed in tui" {
		t.Errorf("user mirror = %q, want you: prefix", s[0].Text)
	}
	if s[1].Text != "assistant reply" {
		t.Errorf("assistant mirror = %q, want bare text", s[1].Text)
	}
}

// TestBridgeIgnoresGroupChats pins the DM-only invariant. Two guards:
//
//   - Structural: the bridge builds its gate with a nil admissions store.
//     That is what closes the original leak — the bridge used to share the
//     bot daemon's on-disk admissions, so an approved group would route
//     into the single TUI session. With no store wired, no on-disk
//     approval can reach the bridge (and the Admissions field is gone, so
//     it can't be re-supplied without a deliberate change here).
//   - Behavioral: a group message — even from the paired owner, even
//     mentioning the bot — never injects a prompt into the session and
//     never retargets (via rememberChat) where the owner's replies go.
func TestBridgeIgnoresGroupChats(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	b := startBridge(t, conn, host, pairedWith("7"))

	// Structural guard: no admissions store means no group can ever be
	// admitted on this surface.
	b.mu.Lock()
	adm := b.gate.admissions
	b.mu.Unlock()
	if adm != nil {
		t.Fatalf("bridge gate has a non-nil admissions store; DM-only invariant broken")
	}

	// Owner DM establishes the reply destination (ChatID "100").
	conn.inbound <- msgFrom("7", "hi from dm")
	host.waitSubmitted(t, 1)

	// Group traffic that must be ignored: a non-owner mentioning the
	// bot, and even the owner speaking in the group.
	conn.inbound <- groupMsg("9", "@tervabot leak into the tui")
	conn.inbound <- groupMsg("7", "owner talking in the group")

	// A second owner DM. Because the connector delivers inbound serially,
	// once this one is submitted the two group messages have already been
	// routed — so asserting on the submissions is race-free.
	conn.inbound <- msgFrom("7", "second dm")
	got := host.waitSubmitted(t, 2)
	if len(got) != 2 || got[0] != "hi from dm" || got[1] != "second dm" {
		t.Fatalf("submissions = %v, want only the two DM prompts (no group text)", got)
	}

	// The reply destination must still be the owner's DM, not the group:
	// the group messages must not have retargeted it via rememberChat.
	b.OnAssistantText("reply to owner")
	s := conn.waitSends(t, 1)
	if s[0].ChatID != "100" {
		t.Fatalf("assistant reply routed to %q, want the owner DM %q (group retargeted the mirror)", s[0].ChatID, "100")
	}
	if s[0].Text != "reply to owner" {
		t.Fatalf("assistant reply text = %q", s[0].Text)
	}
}

func TestBridgePairingNotifiesHost(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	b := startBridge(t, conn, host, Pairing{})

	conn.inbound <- msgFrom("9", "/start")
	conn.waitSends(t, 1) // pairing ack

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		n := len(host.notifies)
		host.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.notifies) == 0 || !strings.Contains(host.notifies[0], "paired with user 9") {
		t.Fatalf("host notifies = %v", host.notifies)
	}
	if st := b.State(); st.PairedID != "9" {
		t.Fatalf("State().PairedID = %q, want 9", st.PairedID)
	}
}

func TestBridgeSendToolPassthrough(t *testing.T) {
	// Unpaired bridge: no chat to address — must refuse, not panic.
	unpaired := startBridge(t, newFakeConnector(Capabilities{}), &fakeHost{}, Pairing{})
	if err := unpaired.SendImage(context.Background(), "/tmp/x.png", ""); err == nil {
		t.Fatalf("SendImage with no paired chat should error")
	}

	// Paired bridge: Start adopts the paired user's DM as the reply
	// chat, so uploads work immediately.
	conn := newFakeConnector(Capabilities{SendsImages: true, SendsFiles: true})
	host := &fakeHost{}
	b := startBridge(t, conn, host, pairedWith("7"))

	conn.inbound <- msgFrom("7", "hello")
	host.waitSubmitted(t, 1)

	if err := b.SendImage(context.Background(), "/tmp/x.png", "cap"); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if err := b.SendDocument(context.Background(), "/tmp/y.pdf", ""); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.images) != 1 || len(conn.files) != 1 {
		t.Fatalf("uploads = %v / %v", conn.images, conn.files)
	}
}

// stopJoinConnector simulates the connector-extension shape of Receive: after
// ctx cancellation it still has teardown to run (extconn closes the driver's
// single chat-tunnel slot there) before returning. released flips only when
// that teardown finished.
type stopJoinConnector struct {
	released atomic.Bool
}

func (c *stopJoinConnector) Name() string { return "stopjoin" }
func (c *stopJoinConnector) Connect(ctx context.Context) (Identity, error) {
	return Identity{Username: "stopjoin-bot"}, nil
}
func (c *stopJoinConnector) Receive(ctx context.Context, handle func(Message)) error {
	<-ctx.Done()
	// The post-cancel teardown a real connector performs (tunnel close,
	// session release). Long enough that an async Stop would reliably
	// return first; trivial next to the 10s join bound.
	time.Sleep(50 * time.Millisecond)
	c.released.Store(true)
	return ctx.Err()
}
func (c *stopJoinConnector) Send(context.Context, Outgoing) error { return nil }
func (c *stopJoinConnector) SendImage(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (c *stopJoinConnector) SendFile(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (c *stopJoinConnector) Typing(context.Context, string) error { return nil }
func (c *stopJoinConnector) Capabilities() Capabilities           { return Capabilities{} }

// TestStopJoinsReceiveLoop pins the synchronous-Stop contract: when Stop
// returns, the receive loop has EXITED and whatever it releases on the way
// out (for a connector extension, the driver's one-chat-session slot) is
// actually free. The async Stop this replaces returned right after the
// context cancel, so a disconnect immediately followed by a reconnect raced
// the teardown and was refused with "a chat session is already open on this
// extension" — the CI flake in TestExtConnectorConnectsDisconnectsReconnects
// that outlived #62/#63's deadline raises.
func TestStopJoinsReceiveLoop(t *testing.T) {
	conn := &stopJoinConnector{}
	b := &Bridge{Connector: conn, Host: &fakeHost{}}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	b.Stop()
	if !conn.released.Load() {
		t.Fatal("Stop returned before the receive loop finished its teardown — a reconnect would race the released-slot invariant")
	}
}

// countingConnector records Connect calls and, once connected, parks in Receive
// until ctx ends. gate, when non-nil, blocks Connect until closed — letting a
// test pin a Start inside Connect (phaseStarting) while other calls race the
// claim, maximizing the double-Connect window the serialization must close.
type countingConnector struct {
	connects atomic.Int32
	gate     chan struct{}
}

func (c *countingConnector) Name() string { return "counting" }
func (c *countingConnector) Connect(ctx context.Context) (Identity, error) {
	c.connects.Add(1)
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		}
	}
	return Identity{Username: "counting-bot"}, nil
}
func (c *countingConnector) Receive(ctx context.Context, handle func(Message)) error {
	<-ctx.Done()
	return ctx.Err()
}
func (c *countingConnector) Send(context.Context, Outgoing) error                    { return nil }
func (c *countingConnector) SendImage(context.Context, string, string, string) error { return nil }
func (c *countingConnector) SendFile(context.Context, string, string, string) error  { return nil }
func (c *countingConnector) Typing(context.Context, string) error                    { return nil }
func (c *countingConnector) Capabilities() Capabilities                              { return Capabilities{} }

// wedgedConnector's Receive never returns, even after ctx ends — the hung
// handle() the Stop wedge-guard exists for.
type wedgedConnector struct{ block chan struct{} }

func (c *wedgedConnector) Name() string { return "wedged" }
func (c *wedgedConnector) Connect(context.Context) (Identity, error) {
	return Identity{Username: "wedged-bot"}, nil
}
func (c *wedgedConnector) Receive(ctx context.Context, handle func(Message)) error {
	<-c.block // ignores ctx entirely
	return nil
}
func (c *wedgedConnector) Send(context.Context, Outgoing) error                    { return nil }
func (c *wedgedConnector) SendImage(context.Context, string, string, string) error { return nil }
func (c *wedgedConnector) SendFile(context.Context, string, string, string) error  { return nil }
func (c *wedgedConnector) Typing(context.Context, string) error                    { return nil }
func (c *wedgedConnector) Capabilities() Capabilities                              { return Capabilities{} }

// TestBridgeConcurrentStartConnectsOnce: N racing Starts must dial exactly once.
// The gate holds the winner inside Connect (phaseStarting) so the losers observe
// the claim and bail — proving serialization happens before Connect, not after.
func TestBridgeConcurrentStartConnectsOnce(t *testing.T) {
	conn := &countingConnector{gate: make(chan struct{})}
	b := &Bridge{Connector: conn, Host: &fakeHost{}}
	t.Cleanup(b.Stop)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = b.Start(context.Background())
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // winner parks in Connect; losers race the claim
	close(conn.gate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Start[%d] = %v, want nil", i, err)
		}
	}
	if got := conn.connects.Load(); got != 1 {
		t.Fatalf("Connect called %d times, want exactly 1", got)
	}
	if !b.Active() {
		t.Fatal("bridge not active after concurrent Start")
	}
}

// TestBridgeStartStopInterleave: a Stop landing while Start is mid-Connect must
// tear the half-started bridge down cleanly — no orphaned loop, no active bridge,
// and both calls return. Connect parks (gate never closed) until the Stop's ctx
// cancel frees it, so the interleave is deterministic.
func TestBridgeStartStopInterleave(t *testing.T) {
	conn := &countingConnector{gate: make(chan struct{})}
	b := &Bridge{Connector: conn, Host: &fakeHost{}, stopTimeout: 2 * time.Second}

	startErr := make(chan error, 1)
	go func() { startErr <- b.Start(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // Start claims phaseStarting, parks in Connect

	stopped := make(chan struct{})
	go func() { b.Stop(); close(stopped) }()

	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop wedged on a start/stop interleave")
	}
	<-startErr // must return, never hang
	if b.Active() {
		t.Fatal("bridge still active after a Start/Stop interleave")
	}
	if b.State().Running {
		t.Fatal("State().Running true after interleave")
	}
	if got := conn.connects.Load(); got != 1 {
		t.Fatalf("Connect calls = %d, want 1", got)
	}
}

// TestBridgeStopTimeoutDoesNotHang: a receive loop that ignores ctx must not
// hang Stop; the bounded wedge guard fires and warns, and the slot frees when
// the loop finally exits.
func TestBridgeStopTimeoutDoesNotHang(t *testing.T) {
	conn := &wedgedConnector{block: make(chan struct{})}
	defer close(conn.block) // release the loop at test end so it settles to idle
	host := &fakeHost{}
	b := &Bridge{Connector: conn, Host: host, stopTimeout: 50 * time.Millisecond}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	b.Stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop blocked %v on a wedged receive loop; the timeout guard failed", d)
	}
	host.mu.Lock()
	warned := false
	for _, n := range host.notifies {
		if strings.Contains(n, "still draining") {
			warned = true
		}
	}
	host.mu.Unlock()
	if !warned {
		t.Fatalf("no 'still draining' warning after Stop timed out; notifies=%v", host.notifies)
	}
}

// TestBridgeStartAfterStopReconnects: after a synchronous Stop frees the slot, a
// fresh Start must reconnect (a second Connect), proving idle was reached.
func TestBridgeStartAfterStopReconnects(t *testing.T) {
	conn := &countingConnector{} // gate nil: Connect returns immediately
	b := &Bridge{Connector: conn, Host: &fakeHost{}, stopTimeout: 2 * time.Second}

	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.Active() {
		t.Fatal("not active after first Start")
	}
	b.Stop()
	if b.Active() {
		t.Fatal("still active after Stop")
	}

	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	t.Cleanup(b.Stop)
	if !b.Active() {
		t.Fatal("not active after reconnect")
	}
	if got := conn.connects.Load(); got != 2 {
		t.Fatalf("Connect called %d times across start/stop/start, want 2", got)
	}
}
