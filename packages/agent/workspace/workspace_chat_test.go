package workspace

// The daemon owns the chat bridge. These tests pin the four claims that made the
// move worth doing, each by an ablation that was watched to fail first:
//
//  1. chat_send_* are DERIVED, not patched: present iff a bridge is active and
//     bound to this session, and they survive an unrelated tool rebuild.
//  2. A second session never sees another's chat tools.
//  3. Origin is the entry point, not a flag: a client Prompt mirrors "you: " out
//     to the phone; a prompt that arrived FROM the phone does not echo itself.
//  4. A bridge never outlives the workspace or the session it is bound to.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// --- a controllable fake connector ---------------------------------------

type fakeChatConn struct {
	mu       sync.Mutex
	sent     []string
	inbound  chan chat.Message
	caps     chat.Capabilities
	stopped  chan struct{}
	stopOnce sync.Once
}

func newFakeChatConn(caps chat.Capabilities) *fakeChatConn {
	return &fakeChatConn{inbound: make(chan chat.Message, 4), caps: caps, stopped: make(chan struct{})}
}

func (c *fakeChatConn) Name() string { return "fakechat" }

func (c *fakeChatConn) Connect(ctx context.Context) (chat.Identity, error) {
	return chat.Identity{ID: "b1", Username: "fakebot"}, nil
}

func (c *fakeChatConn) Receive(ctx context.Context, handle func(chat.Message)) error {
	defer c.stopOnce.Do(func() { close(c.stopped) })
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-c.inbound:
			handle(m)
		}
	}
}

func (c *fakeChatConn) Send(ctx context.Context, out chat.Outgoing) error {
	c.mu.Lock()
	c.sent = append(c.sent, out.Text)
	c.mu.Unlock()
	return nil
}

func (c *fakeChatConn) SendImage(ctx context.Context, chatID, path, caption string) error { return nil }
func (c *fakeChatConn) SendFile(ctx context.Context, chatID, path, caption string) error  { return nil }
func (c *fakeChatConn) Typing(ctx context.Context, chatID string) error                   { return nil }
func (c *fakeChatConn) Capabilities() chat.Capabilities                                   { return c.caps }

func (c *fakeChatConn) outbound() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
}

// dm delivers one message from the paired owner.
func (c *fakeChatConn) dm(text string) {
	c.inbound <- chat.Message{ID: "m1", ChatID: "c1", ChatKind: "dm", UserID: "u1", Username: "owner", Text: text}
}

// startFakeBridge wires a live bridge onto w bound to sessID, bypassing the
// service registry (which is process-global and shared across tests).
func startFakeBridge(t *testing.T, w *Workspace, sessID string, conn *fakeChatConn) *chat.Bridge {
	t.Helper()
	host := &chatWsHost{w: w, service: conn.Name()}
	b := &chat.Bridge{Connector: conn, Host: host, Pairing: chat.Pairing{
		AllowedUserID: "u1", Save: func(string) error { return nil },
	}}
	if err := b.Start(w.ctx); err != nil {
		t.Fatalf("bridge start: %v", err)
	}
	w.chat.mu.Lock()
	w.chat.bridges = map[string]*boundBridge{
		conn.Name(): {bridge: b, sess: sessID, state: chatStateConnected},
	}
	w.chat.mu.Unlock()
	t.Cleanup(b.Stop)
	return b
}

// --- 1 + 2: the tools are derived, per-session ---------------------------

func TestChatToolsAreDerivedFromTheBoundBridge(t *testing.T) {
	w := &Workspace{ctx: context.Background(), sessions: map[string]*wsSession{}}
	conn := newFakeChatConn(chat.Capabilities{SendsImages: true, SendsFiles: true})

	if _, _, ok := w.chatBound("s1"); ok {
		t.Fatal("a bridge is bound before one was started; the probe is broken")
	}

	startFakeBridge(t, w, "s1", conn)

	b, caps, ok := w.chatBound("s1")
	if !ok || b == nil {
		t.Fatal("bridge is not bound to its session")
	}
	if !caps.SendsImages || !caps.SendsFiles {
		t.Fatalf("capabilities not read from the connector: %+v", caps)
	}

	// The second session must not see the first's chat tools.
	if _, _, ok := w.chatBound("s2"); ok {
		t.Fatal("a bridge bound to s1 is visible from s2: chat tools would leak across sessions")
	}

	// Disconnecting makes the derivation false again — nothing is patched.
	b.Stop()
	if _, _, ok := w.chatBound("s1"); ok {
		t.Fatal("bridge still reports bound after Stop; the tools would outlive the connection")
	}
}

// A bridge that is registered but not receiving must not advertise tools: the
// model would get chat_send_image and a guaranteed failure.
func TestChatToolsAbsentWhileConnecting(t *testing.T) {
	w := &Workspace{ctx: context.Background(), sessions: map[string]*wsSession{}}
	w.chat.bridges = map[string]*boundBridge{
		"fakechat": {sess: "s1", state: chatStateConnecting}, // dialing: no bridge yet
	}
	if _, _, ok := w.chatBound("s1"); ok {
		t.Fatal("a dialing bridge advertised chat tools")
	}
}

// --- 3: origin is the entry point ---------------------------------------

// A prompt that arrived FROM the phone must not be echoed back to it. The bridge
// submits through wsSession.prompt (the internal seam); only Workspace.Prompt
// (the client-facing verb) mirrors "you: ".
func TestChatOriginatedPromptDoesNotEchoItself(t *testing.T) {
	w, s, cl := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)
	_ = cl

	conn.dm("hello from the phone")

	// The turn must run: the DM became a prompt.
	waitFor(t, "the DM to reach the model", func() bool {
		return len(s.agent.Messages()) > 0
	})

	for _, out := range conn.outbound() {
		if strings.HasPrefix(out, "you: ") {
			t.Fatalf("a chat-originated prompt echoed itself back: %q", out)
		}
	}
}

// A client's prompt (TUI, web, remote carrier) DOES mirror out, so the phone
// thread stays a complete record of the conversation.
func TestClientPromptMirrorsToTheChat(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)

	if err := w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{Text: "typed locally"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	waitFor(t, `the "you: " mirror`, func() bool {
		for _, out := range conn.outbound() {
			if out == "you: typed locally" {
				return true
			}
		}
		return false
	})
}

// --- 4: lifetime --------------------------------------------------------

func TestWorkspaceCloseStopsTheBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &Workspace{ctx: ctx, cancel: cancel, sessions: map[string]*wsSession{}}
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)

	w.chatStopAll()

	select {
	case <-conn.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the connector's receive goroutine outlived the workspace")
	}
}

func TestClosingTheBoundSessionStopsTheBridge(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)

	// Drive the real teardown path, not chatStopForSession directly: the wiring
	// from close() is the thing that can rot.
	s.close()

	select {
	case <-conn.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("a mirror survived the session it was bound to")
	}
	if _, _, ok := w.chatBound("s1"); ok {
		t.Fatal("the bridge is still registered after its session closed")
	}
}

// --- the one-bridge guard ------------------------------------------------

func TestOnlyOneBridgePerWorkspace(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)

	// A DIFFERENT, fully configured service: the cap is per workspace, not per
	// service, because extconn's host slot is process-global.
	registerFakeService(t, "fakechat-two")

	err := w.chatConnect("s1", "fakechat-two")
	if err == nil {
		t.Fatal("a second bridge was admitted; extconn's host slot is process-global")
	}
	if !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("error does not explain the cap: %v", err)
	}
}

// registerFakeService adds a configured service to the process-global chat
// registry for the duration of one test. Named uniquely so concurrent tests in
// this package never see each other's rows.
func registerFakeService(t *testing.T, name string) {
	t.Helper()
	enabled := &atomic.Bool{}
	enabled.Store(true)
	chat.Register(chat.Service{
		Name:       name,
		Configured: func(string) bool { return enabled.Load() },
		NewConnector: func(string, func(string)) (chat.Connector, chat.Pairing, error) {
			return newFakeChatConn(chat.Capabilities{}), chat.Pairing{}, nil
		},
	})
	t.Cleanup(func() { enabled.Store(false) })
}

// --- helpers -------------------------------------------------------------

func waitFor(t *testing.T, what string, pred func() bool, diag ...func() string) {
	t.Helper()
	// Generous ceiling: these predicates poll on subprocess / connection state
	// (connector spawn, hello handshake, bridge status) that is slow under CI
	// load and the race detector, so a tight deadline flakes. The happy path
	// returns the instant the predicate holds; a real failure still fails, just
	// a little later.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A bare "timed out" hides whether the state machine was still moving or
	// had already failed (a refused dial parks in "error" forever, and this
	// deadline is then blamed for it) — dump the caller's diagnostics so a CI
	// log answers that without a reproduction.
	detail := ""
	for _, d := range diag {
		detail += "\n  " + d()
	}
	t.Fatalf("timed out waiting for %s%s", what, detail)
}

// chatTestWorkspace builds a workspace with one live session backed by a fake
// provider that answers immediately.
func chatTestWorkspace(t *testing.T, id string) (*Workspace, *wsSession, *fakeChatClient) {
	t.Helper()
	cl := &fakeChatClient{}
	root := testsupport.TempDir(t)
	w := &Workspace{ctx: context.Background(), root: root, cwd: root,
		sessions: map[string]*wsSession{}, diag: func(string) {}}
	sf, err := core.NewSessionAtPath(filepath.Join(root, id+".jsonl"), root, "fake", "fake-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSession()
	s.id = id
	s.ws = w
	s.sess = sf
	s.cwd = root
	s.agent = core.NewAgent(cl, "fake-model", "", core.Registry{})
	w.sessions[id] = s
	return w, s, cl
}

type fakeChatClient struct{}

func (fakeChatClient) Name() string { return "fake" }

func (fakeChatClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ack"}},
		}}
	}()
	return out, nil
}

// --- the central claim: the tools are DERIVED into the registry ----------

// injectExtraTools is the single place chat_send_* enters the model's view.
// rebuildTools re-runs it on every extension reload, MCP toggle and trust flip,
// so a re-derivation cannot desynchronize the way the TUI's snapshot-and-merge
// patch could. Deriving onto a FRESH registry is exactly what a rebuild does.
func TestInjectExtraToolsDerivesChatTools(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")

	derive := func() core.Registry {
		r := build.Resolved{ToolRegistry: core.Registry{}, CWD: s.cwd}
		w.injectExtraTools(s, &r, build.Args{NoTools: true})
		return r.ToolRegistry
	}

	// Disconnected: the model must not see tools it cannot use.
	if reg := derive(); reg["chat_send_image"] != nil || reg["chat_send_file"] != nil {
		t.Fatal("chat tools were injected with no bridge connected")
	}

	conn := newFakeChatConn(chat.Capabilities{SendsImages: true, SendsFiles: true})
	startFakeBridge(t, w, "s1", conn)

	reg := derive()
	if reg["chat_send_image"] == nil {
		t.Fatal("chat_send_image was not derived onto a fresh registry: an extension reload would drop it")
	}
	if reg["chat_send_file"] == nil {
		t.Fatal("chat_send_file was not derived onto a fresh registry")
	}

	// A second, independent derivation (what an ext reload triggers) keeps them.
	if reg2 := derive(); reg2["chat_send_image"] == nil {
		t.Fatal("chat tools did not survive a second derivation")
	}

	// A session the bridge is NOT bound to never sees them.
	other := newTestSession()
	other.id, other.ws, other.cwd = "s2", w, s.cwd
	r := build.Resolved{ToolRegistry: core.Registry{}, CWD: other.cwd}
	w.injectExtraTools(other, &r, build.Args{NoTools: true})
	if r.ToolRegistry["chat_send_image"] != nil {
		t.Fatal("a session without the bridge saw another session's chat tools")
	}
}

// Capabilities gate each tool independently: a connector that sends images but
// not files must not advertise chat_send_file.
func TestChatToolsHonourConnectorCapabilities(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{SendsImages: true, SendsFiles: false})
	startFakeBridge(t, w, "s1", conn)

	r := build.Resolved{ToolRegistry: core.Registry{}, CWD: s.cwd}
	w.injectExtraTools(s, &r, build.Args{NoTools: true})

	if r.ToolRegistry["chat_send_image"] == nil {
		t.Fatal("chat_send_image missing though the connector sends images")
	}
	if r.ToolRegistry["chat_send_file"] != nil {
		t.Fatal("chat_send_file injected though the connector cannot send files")
	}
}

// --- the escape does not exist on this side of the boundary --------------

// A paired DM beginning with "!" used to run an ungated shell command on the
// host: chat.Bridge routed prompts to Interactive.SubmitOrQueue, whose first act
// was shellEscapeCommand. The daemon-side Host has no such path — a prompt is a
// prompt — and this pins it where the bridge now lives. The structural guard
// (one call site) lives in packages/agent/modes.
func TestPairedDMCannotShellEscape(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	conn := newFakeChatConn(chat.Capabilities{})
	startFakeBridge(t, w, "s1", conn)

	marker := filepath.Join(s.cwd, "pwned.txt")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker existed before the DM; the test is vacuous")
	}

	dm := "!touch " + marker
	conn.dm(dm)

	// It must reach the model, verbatim, "!" and all.
	waitFor(t, "the DM to reach the model as a prompt", func() bool {
		for _, m := range s.agent.Messages() {
			if m.Role != provider.RoleUser {
				continue
			}
			for _, blk := range m.Content {
				if tb, ok := blk.(provider.TextBlock); ok && tb.Text == dm {
					return true
				}
			}
		}
		return false
	})

	// And it must not have run. This half is belt-and-braces: there is no shell
	// escape anywhere in the workspace, so it cannot fire. The load-bearing
	// assertion is the one above — the text reaches the model verbatim.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("a paired DM starting with ! executed a shell command on the host")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
