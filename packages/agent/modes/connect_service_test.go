package modes

// /connect service-awareness: any registered chat service — a
// compiled-in connector, a connector extension, a dev manifest — is
// reachable by name, with provenance tags on the status surfaces. The
// fake service below stands in for a connector extension (Kind =
// "extension"); its Configured gate is flipped per-test so the shared
// registry never leaks picker rows into unrelated tests (the alias
// test asserts the nothing-configured message).

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/tui/tuitest"
)

// fakeChatConn is a minimal chat.Connector: connects instantly with a
// fixed identity and receives until the consumer leaves.
type fakeChatConn struct{}

func (fakeChatConn) Name() string { return "fakechat" }

func (fakeChatConn) Connect(ctx context.Context) (chat.Identity, error) {
	return chat.Identity{ID: "b1", Username: "fakebot"}, nil
}

func (fakeChatConn) Receive(ctx context.Context, handle func(chat.Message)) error {
	<-ctx.Done()
	return ctx.Err()
}

func (fakeChatConn) Send(ctx context.Context, out chat.Outgoing) error { return nil }
func (fakeChatConn) SendImage(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (fakeChatConn) SendFile(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (fakeChatConn) Typing(ctx context.Context, chatID string) error { return nil }
func (fakeChatConn) Capabilities() chat.Capabilities                 { return chat.Capabilities{} }

var (
	fakeChatOnce    sync.Once
	fakeChatEnabled atomic.Bool
)

// enableFakeChatService registers the fake service (once, globally)
// and turns its Configured gate on for the duration of one test.
func enableFakeChatService(t *testing.T) {
	t.Helper()
	fakeChatOnce.Do(func() {
		chat.Register(chat.Service{
			Name:       "fakechat",
			Kind:       "extension",
			Configured: func(string) bool { return fakeChatEnabled.Load() },
			NewConnector: func(tervaHome string, warn func(string)) (chat.Connector, chat.Pairing, error) {
				return fakeChatConn{}, chat.Pairing{Save: func(string) error { return nil }}, nil
			},
		})
	})
	fakeChatEnabled.Store(true)
	t.Cleanup(func() { fakeChatEnabled.Store(false) })
}

// TestInteractiveConnectNamedService drives the whole named-service
// path on the real event loop: /connect <name> starts the bridge on
// that service (provenance-tagged), /connect status reports it, and
// /connect disconnect stops it.
func TestInteractiveConnectNamedService(t *testing.T) {
	enableFakeChatService(t)
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("/connect fakechat\r")
	h.waitText("fakechat (extension) connected as @fakebot")

	h.term.Type("/connect status\r")
	h.waitText("fakechat (extension): connected (tui bridge) as @fakebot")

	h.term.Type("/connect disconnect\r")
	h.waitText("fakechat disconnected")
}

// TestInteractiveConnectUnknownService: a name that resolves to no
// registered service fails with the explanatory error, listing what
// IS available.
func TestInteractiveConnectUnknownService(t *testing.T) {
	enableFakeChatService(t)
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("/connect nosuch\r")
	h.waitScreen("unknown-connector error naming the alternatives", func(s *tuitest.Screen) bool {
		txt := s.Text()
		return strings.Contains(txt, "unknown /connect action: nosuch") &&
			strings.Contains(txt, "connector name")
	})
}
