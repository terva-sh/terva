package chat

import (
	"context"
	"strings"
	"sync"
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
