package workspace

// 3b.3: the workspace owns extconn's process-global host slot.
//
// extconn.Conn resolves its live extension process through the bound Host at
// dial time — and until the workspace bound one, `/connect <ext-connector>`
// had NEVER worked against a workspace (only `terva bot`'s CLI setup bound the
// slot). These tests pin the binding lifecycle: connect binds, every path that
// removes a bridge releases, a corpse from a failed dial doesn't wedge
// /connect, and an internal host with no bound session resolves NOTHING —
// never the default session.
//
// Every rule here was ablated and watched to fail before it was trusted.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/extconn"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// registerConnService registers a service that hands out ONE shared connector
// instance, so a test keeps a handle on the connection it drives. Cleanup
// flips Configured off — the chat registry is process-global and has no
// unregister.
func registerConnService(t *testing.T, name string, conn chat.Connector) {
	t.Helper()
	enabled := &atomic.Bool{}
	enabled.Store(true)
	chat.Register(chat.Service{
		Name:       name,
		Configured: func(string) bool { return enabled.Load() },
		NewConnector: func(string, func(string)) (chat.Connector, chat.Pairing, error) {
			return conn, chat.Pairing{}, nil
		},
	})
	t.Cleanup(func() { enabled.Store(false) })
}

// --- connect binds, teardown releases -------------------------------------

func TestConnectBindsTheExtHostAndSessionCloseReleasesIt(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	t.Cleanup(w.chatStopAll)
	registerConnService(t, "slot-owner", newFakeChatConn(chat.Capabilities{}))

	if err := w.chatConnect("s1", "slot-owner"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// The bind is synchronous with arming the dial, under the registry lock —
	// the dial itself consults the slot, so binding any later would race it.
	if extconn.BoundHost() == nil {
		t.Fatal("connect did not bind the extension host slot")
	}
	waitFor(t, "the dial", func() bool { return w.chatView().Bridge.State == chatStateConnected })

	s.close()
	if extconn.BoundHost() != nil {
		t.Fatal("closing the bound session left the extension host slot bound")
	}
}

func TestWorkspaceCloseReleasesTheExtHostSlot(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	registerConnService(t, "slot-wsclose", newFakeChatConn(chat.Capabilities{}))

	if err := w.chatConnect("s1", "slot-wsclose"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitFor(t, "the dial", func() bool { return w.chatView().Bridge.State == chatStateConnected })

	w.chatStopAll()
	if extconn.BoundHost() != nil {
		t.Fatal("workspace teardown left the extension host slot bound")
	}
}

// --- the host resolves the BOUND session, per call -------------------------

// plantedDefaultWorkspace builds a workspace whose one session, "s1", is
// discoverable as the DEFAULT: its transcript sits where LatestSession scans,
// so resolve("") finds it. The guard tests need a reachable leak target — a
// "resolves nothing" assertion against a harness where resolve("") fails
// anyway would prove nothing — so the helper verifies its own instrument.
func plantedDefaultWorkspace(t *testing.T) (*Workspace, *wsSession) {
	t.Helper()
	root := testsupport.TempDir(t)
	w := &Workspace{ctx: context.Background(), root: root, cwd: root,
		sessions: map[string]*wsSession{}, diag: func(string) {}}
	dir := core.SessionsDir(root, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sf, err := core.NewSessionAtPath(filepath.Join(dir, "s1.jsonl"), root, "fake", "fake-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSession()
	s.id = "s1"
	s.ws = w
	s.sess = sf
	s.cwd = root
	s.agent = core.NewAgent(&fakeChatClient{}, "fake-model", "", core.Registry{})
	w.sessions["s1"] = s

	if got, rerr := w.resolve(""); rerr != nil || got != s {
		t.Fatalf("harness: resolve(\"\") must find s1 for these tests to prove anything (got %v, err %v)", got, rerr)
	}
	return w, s
}

// Binding a session's *extensions.Manager directly would go stale twice over:
// a rebind moves the mirror to a session with a DIFFERENT manager, and a
// rematerialized session builds a fresh one. The host must re-resolve per
// call — and resolve nothing at all once unbound, because resolve("") is the
// default-session affordance for clients, not for internal plumbing.
func TestWsExtHostResolvesTheBoundSessionOnly(t *testing.T) {
	w, s1 := plantedDefaultWorkspace(t)
	s2 := newTestSession()
	s2.id = "s2"
	s2.ws = w
	w.sessions["s2"] = s2

	m1 := extensions.New(w.root, w.root, "0.0.0-test", "fake", "fake-model", build.NonInteractiveExtHooks{})
	m2 := extensions.New(w.root, w.root, "0.0.0-test", "fake", "fake-model", build.NonInteractiveExtHooks{})
	s1.extMgr, s2.extMgr = m1, m2

	w.chat.mu.Lock()
	w.chat.bridges = map[string]*boundBridge{"svc": {sess: "s1", state: chatStateConnected}}
	w.chat.mu.Unlock()

	h := &wsExtHost{w: w, service: "svc"}
	if h.mgr() != m1 {
		t.Fatal("the ext host did not resolve the bound session's manager")
	}

	// A rebind moves the crash-recovery respawn path with the mirror.
	w.chat.mu.Lock()
	w.chat.bridges["svc"].sess = "s2"
	w.chat.mu.Unlock()
	if h.mgr() != m2 {
		t.Fatal("the ext host did not follow the rebind")
	}

	// Unbound: NOTHING resolves. s1 is the planted default, so a leaked
	// resolve("") here would hand back m1 — the wrong session's extension
	// subsystem — not fail.
	w.chat.mu.Lock()
	delete(w.chat.bridges, "svc")
	w.chat.mu.Unlock()
	if h.mgr() != nil {
		t.Fatal("an unbound ext host resolved a session — the default-session affordance leaked into internal plumbing")
	}
}

// An inbound DM delivered in the window between the bridge leaving the
// registry and its receive loop stopping must be DROPPED. Without the guard,
// chatWsHost.session() is "" and resolve("") routes the DM into whatever
// session is latest on disk.
func TestUnboundChatHostDropsInboundDMs(t *testing.T) {
	w, s := plantedDefaultWorkspace(t)

	h := &chatWsHost{w: w, service: "ghost"} // no bridge registered for it
	h.SubmitOrQueue("!touch pwned", nil)

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := len(s.agent.Messages()); n != 0 {
			t.Fatalf("a DM from an unbound bridge reached the default session (%d messages)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- a failed dial must not wedge /connect ---------------------------------

// flakyChatConn fails its first Connect, then behaves. The flake lives on
// Connect, not NewConnector, because chatView probes NewConnector for pairing
// state on every render — a count there would burn down on pane refreshes.
type flakyChatConn struct {
	*fakeChatConn
	failures atomic.Int32
}

func (c *flakyChatConn) Connect(ctx context.Context) (chat.Identity, error) {
	if c.failures.Add(-1) >= 0 {
		return chat.Identity{}, errors.New("service unreachable")
	}
	return c.fakeChatConn.Connect(ctx)
}

func TestReconnectAfterAFailedDial(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	t.Cleanup(w.chatStopAll)
	conn := &flakyChatConn{fakeChatConn: newFakeChatConn(chat.Capabilities{})}
	conn.failures.Store(1)
	registerConnService(t, "flaky-dial", conn)

	if err := w.chatConnect("s1", "flaky-dial"); err != nil {
		t.Fatalf("connect (arming the dial must succeed; the failure is async): %v", err)
	}
	waitFor(t, "the failed dial to surface", func() bool {
		return w.chatView().Bridge.State == chatStateError
	})
	if extconn.BoundHost() != nil {
		t.Fatal("a failed dial left the extension host slot bound")
	}

	// The corpse stays on the pane, but a retry must replace it — not bounce
	// off "already connected" until the user finds /connect disconnect.
	if err := w.chatConnect("s1", "flaky-dial"); err != nil {
		t.Fatalf("reconnect after a failed dial: %v", err)
	}
	waitFor(t, "the retry to connect", func() bool {
		return w.chatView().Bridge.State == chatStateConnected
	})
}

// --- disconnect racing a slow dial ------------------------------------------

// gatedChatConn parks Connect until released, so the test can interleave a
// disconnect INSIDE the dial deterministically.
type gatedChatConn struct {
	*fakeChatConn
	release chan struct{}
}

func (c *gatedChatConn) Connect(ctx context.Context) (chat.Identity, error) {
	<-c.release
	return c.fakeChatConn.Connect(ctx)
}

func TestDisconnectDuringDialStopsTheOrphanBridge(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	conn := &gatedChatConn{fakeChatConn: newFakeChatConn(chat.Capabilities{}), release: make(chan struct{})}
	registerConnService(t, "slow-dial", conn)

	if err := w.chatConnect("s1", "slow-dial"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// The dial goroutine is parked inside Connect; disconnect wins the race.
	if err := w.chatDisconnect(); err != nil {
		t.Fatalf("disconnect during dial: %v", err)
	}
	close(conn.release)

	// The late bridge finds its registry entry gone and must stop itself: its
	// receive loop would otherwise hold the connector until workspace close,
	// invisible to every pane.
	select {
	case <-conn.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("a bridge that lost the dial/disconnect race kept receiving")
	}
	if got := w.chatView().Bridge.State; got != chatStateIdle {
		t.Fatalf("pane shows %q after the orphan stopped, want idle", got)
	}
	if extconn.BoundHost() != nil {
		t.Fatal("the extension host slot is bound with no bridge registered")
	}
}

// --- end to end: a real connector extension --------------------------------

// writeConnectorExt writes a /bin/sh extension that registers the connector
// role and answers the tunneled connector protocol: hello on chat_open,
// connected on connect. The same wire-level fixture extconn's own e2e uses,
// here driven through the WORKSPACE's connect path.
func writeConnectorExt(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.0","exec":"./run.sh","connector":true}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' '{"type":"hello","name":"` + name + `","version":"1.0"}'
printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"` + name + `","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"bot-1","username":"e2e-bot"}}\n' "$sid"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The 3b.3 acceptance, on the real stack: /connect <ext-connector> against a
// workspace succeeds — extconn dials through the host the workspace bound,
// resolving the bound session's live extension process — then disconnect
// releases the slot and a second connect re-binds cleanly through a fresh
// tunnel to the same extension process.
func TestExtConnectorConnectsDisconnectsReconnects(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	t.Cleanup(w.chatStopAll)

	const name = "wsx-e2e"
	dir := filepath.Join(w.root, "ext-src", name)
	writeConnectorExt(t, dir, name)

	mgr := extensions.New(w.root, w.root, "0.0.0-test", "fake", "fake-model", build.NonInteractiveExtHooks{})
	for _, e := range mgr.LoadExplicit(context.Background(), []string{dir}) {
		t.Fatalf("extension load: %v", e)
	}
	t.Cleanup(func() { mgr.Stop(2 * time.Second) })
	// The connector extension is a real subprocess; its spawn + ready handshake
	// varies widely under CI load + -race. A tight grace here was the flake
	// source (TestExtConnectorConnectsDisconnectsReconnects). WaitForReady is
	// best-effort — it returns as soon as the ext is ready and only waits the
	// full grace when the ext is genuinely slow to come up.
	mgr.WaitForReady(testsupport.ExtReadyGrace)
	s.extMgr = mgr

	// Normally RegisterDiscovered does this at CLI startup, from the GLOBAL
	// extensions dir. The registry is process-global with no unregister; the
	// unique name keeps it inert for other tests.
	extconn.RegisterService(name)

	if err := w.chatConnect("s1", name); err != nil {
		t.Fatalf("connect: %v", err)
	}
	chatDiag := func() string {
		v := w.chatView()
		return fmt.Sprintf("bridge state=%q err=%q user=%q", v.Bridge.State, v.Bridge.Error, v.Bridge.Username)
	}
	waitFor(t, "the connector extension to come up", func() bool {
		v := w.chatView()
		return v.Bridge.State == chatStateConnected && v.Bridge.Username == "e2e-bot"
	}, chatDiag)

	if err := w.chatDisconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if extconn.BoundHost() != nil {
		t.Fatal("disconnect left the extension host slot bound")
	}

	if err := w.chatConnect("s1", name); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	waitFor(t, "the second dial", func() bool {
		return w.chatView().Bridge.State == chatStateConnected
	}, chatDiag)
	if err := w.chatDisconnect(); err != nil {
		t.Fatalf("second disconnect: %v", err)
	}
}
