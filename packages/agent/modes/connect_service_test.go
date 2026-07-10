package modes

// /connect is a client of the daemon's chat surface. The TUI owns no bridge, no
// chat.Host and no connector — it sends surface actions and renders the pane it
// gets back. These tests drive the real event loop and assert on the wire, which
// is the only thing left to assert on.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui/tuitest"
)

func startWithChat(t *testing.T, v ctrlproto.ChatView) (*harness, *fakeCarrier) {
	t.Helper()
	fc := newFakeCarrier()
	fc.chat = v
	h := startInteractive(t, func(c *InteractiveConfig) {
		c.Carrier = fc
		c.Ready = true
	})
	return h, fc
}

// A named connect becomes surface.action{id:"chat", action:"connect", name}.
// The TUI does not dial; the daemon does, asynchronously.
func TestConnectNamedServiceIssuesASurfaceAction(t *testing.T) {
	h, fc := startWithChat(t, ctrlproto.ChatView{
		Services: []ctrlproto.ChatServiceInfo{{Name: "fakechat", Kind: "extension", Configured: true, Paired: true}},
		Bridge:   ctrlproto.ChatBridgeState{State: "idle"},
	})

	h.term.Type("/connect fakechat\r")

	act := recv(t, fc.surfActs, "chat surface action")
	if act.id != "chat" || act.action != "connect" {
		t.Fatalf("got surface action %+v, want chat/connect", act)
	}
	if act.args["name"] != "fakechat" {
		t.Fatalf("connect named %q, want fakechat", act.args["name"])
	}
}

func TestDisconnectIssuesASurfaceAction(t *testing.T) {
	h, fc := startWithChat(t, ctrlproto.ChatView{
		Services: []ctrlproto.ChatServiceInfo{{Name: "fakechat", Configured: true}},
		Bridge:   ctrlproto.ChatBridgeState{State: "connected", Connector: "fakechat", Username: "fakebot"},
	})

	h.term.Type("/connect disconnect\r")

	act := recv(t, fc.surfActs, "chat surface action")
	if act.id != "chat" || act.action != "disconnect" {
		t.Fatalf("got %+v, want chat/disconnect", act)
	}
}

// An unknown name never reaches the daemon: the TUI resolves it against the
// surface's service list, since it no longer has a chat registry to consult.
func TestUnknownServiceNeverReachesTheDaemon(t *testing.T) {
	h, fc := startWithChat(t, ctrlproto.ChatView{
		Services: []ctrlproto.ChatServiceInfo{{Name: "fakechat", Kind: "extension", Configured: true}},
		Bridge:   ctrlproto.ChatBridgeState{State: "idle"},
	})

	h.term.Type("/connect nosuch\r")
	h.waitScreen("unknown-connector error", func(s *tuitest.Screen) bool {
		return strings.Contains(s.Text(), "unknown /connect action: nosuch")
	})

	select {
	case act := <-fc.surfActs:
		t.Fatalf("an unknown connector was forwarded to the daemon: %+v", act)
	default:
	}
}

// /connect status renders the daemon's view.
func TestConnectStatusRendersTheDaemonView(t *testing.T) {
	h, _ := startWithChat(t, ctrlproto.ChatView{
		Services: []ctrlproto.ChatServiceInfo{{Name: "fakechat", Configured: true}},
		Bridge: ctrlproto.ChatBridgeState{
			State: "connected", Connector: "fakechat", Username: "fakebot", PairedID: "u1",
		},
	})

	h.term.Type("/connect status\r")
	h.waitText("fakechat: connected as @fakebot")
}

// The bot-daemon conflict is the daemon's to report; the TUI just shows it.
func TestConnectStatusReportsTheBotDaemonConflict(t *testing.T) {
	h, _ := startWithChat(t, ctrlproto.ChatView{
		Services:  []ctrlproto.ChatServiceInfo{{Name: "fakechat", Configured: true}},
		Bridge:    ctrlproto.ChatBridgeState{State: "idle"},
		DaemonPID: 4242,
	})

	h.term.Type("/connect status\r")
	h.waitScreen("bot-daemon conflict", func(s *tuitest.Screen) bool {
		return strings.Contains(s.Text(), "4242")
	})
}

// --- the status bar reads the mirror, and does not lie -------------------

// The bridge binds to the session it was connected from. A status bar naming a
// bridge that mirrors some OTHER session would be claiming this session's turns
// reach the phone. They don't.
func TestStatusBarNamesTheBridgeOnlyForTheBoundSession(t *testing.T) {
	i := &Interactive{}

	i.carrierChat = ctrlproto.ChatView{Bridge: ctrlproto.ChatBridgeState{
		State: "connected", Connector: "fakechat", Session: "other-session",
	}}
	i.cfg.CarrierSession = "this-session"
	if got := i.chatBridgeName(); got != "" {
		t.Fatalf("status bar named %q for a bridge bound elsewhere", got)
	}

	i.carrierChat.Bridge.Session = "this-session"
	if got := i.chatBridgeName(); got != "fakechat" {
		t.Fatalf("status bar = %q, want fakechat for the bound session", got)
	}

	// A dial in flight is not a connection.
	i.carrierChat.Bridge.State = "connecting"
	if got := i.chatBridgeName(); got != "" {
		t.Fatalf("status bar named %q while still connecting", got)
	}
}

// The picker is a pure function of the daemon's view.
func TestConnectMenuItemsRenderFromTheSurface(t *testing.T) {
	idle := ctrlproto.ChatView{Services: []ctrlproto.ChatServiceInfo{
		{Name: "configured", Configured: true, Paired: true},
		{Name: "unconfigured", Configured: false},
	}}
	var labels []string
	for _, it := range connectMenuItems(idle, "s1") {
		labels = append(labels, it.Label)
	}
	joined := strings.Join(labels, ",")
	if !strings.Contains(joined, "connect configured") {
		t.Fatalf("configured service missing from the picker: %v", labels)
	}
	if strings.Contains(joined, "unconfigured") {
		t.Fatalf("an unconfigured service was offered: %v", labels)
	}

	connected := ctrlproto.ChatView{Bridge: ctrlproto.ChatBridgeState{
		State: "connected", Connector: "fakechat", Session: "s1",
	}}
	items := connectMenuItems(connected, "s1")
	if len(items) == 0 || items[0].Action != "disconnect" {
		t.Fatalf("connected picker does not lead with disconnect: %+v", items)
	}
	for _, it := range items {
		if it.Action == "rebind" {
			t.Fatal("rebind offered for a bridge already bound to this session")
		}
	}

	// Bound elsewhere: offer to move it, explicitly. Never move it silently.
	connected.Bridge.Session = "s2"
	found := false
	for _, it := range connectMenuItems(connected, "s1") {
		if it.Action == "rebind" {
			found = true
		}
	}
	if !found {
		t.Fatal("no rebind offered for a bridge bound to another session")
	}
}
