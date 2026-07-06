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
