package extdriver

// The connector role (extension protocol 5, experimental): one extension
// process that is ALSO a chat connector. This file is the driver half —
// the register gate that enforces the manifest's Connector consent flag,
// and the TUNNEL that carries the connector protocol's frames through
// the extension wire. The driver never parses those inner frames: the
// connector protocol (packages/agent/connproto) is spoken by
// chat/connhost on the host side and connsdk inside the extension, so
// exactly one implementation of that wire exists per side no matter
// what carries it. See docs/proposals/connector-extensions.md.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"terva.sh/terva/packages/agent/extproto"
)

// chatFrameBuffer bounds how many inner frames can queue between the
// read loop and the tunnel's consumer. The read loop must never block
// on it — it also delivers tool results — and it must never silently
// drop either (a missing frame desyncs the inner protocol), so overflow
// kills the session loudly instead.
const chatFrameBuffer = 256

// ConnectorExtension returns the named extension iff it has registered
// the connector role (register_connector accepted under the manifest's
// Connector consent flag). The chat adapter resolves through this at
// Connect time, so a merely-installed extension is invisible to the chat
// stack until the role actually registers.
func (d *Driver) ConnectorExtension(name string) (*Extension, bool) {
	d.mu.RLock()
	ext, ok := d.ext[name]
	d.mu.RUnlock()
	if !ok {
		return nil, false
	}
	ext.mu.Lock()
	registered := ext.connectorRole
	ext.mu.Unlock()
	if !registered {
		return nil, false
	}
	return ext, true
}

// registerConnector records a register_connector frame. The manifest's
// Connector flag is the consent gate: without it the role is refused
// (logged, ignored) — a tool extension must not be able to quietly grow
// into a message source after install.
func (d *Driver) registerConnector(ext *Extension) {
	if !ext.Manifest.Connector {
		fmt.Fprintf(ext.logFile, "[terva] register_connector refused: manifest does not declare \"connector\": true\n")
		return
	}
	ext.mu.Lock()
	ext.connectorRole = true
	ext.mu.Unlock()
}

// ConnectorRole reports whether the extension registered the connector
// role (and the manifest consented).
func (e *Extension) ConnectorRole() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connectorRole
}

// DataDir returns the extension's writable state directory (the same
// value announced in hello_ack). The chat adapter's attachment
// containment resolves against it.
func (e *Extension) DataDir() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dataDir
}

// ChatTunnel is one chat session's frame carrier: an opaque, ordered,
// bidirectional stream of connector-protocol frames riding `chat`
// envelopes on the extension wire. It satisfies connhost.FrameConn.
// Sessions are serial — one live tunnel per extension — and every
// envelope carries the session id minted at OpenChat, so frames from a
// torn-down session can never bleed into its successor.
type ChatTunnel struct {
	ext *Extension
	id  string

	// frames is the read loop's hand-off of inner frames; ONLY the
	// read-loop goroutine sends to or closes it (on chat_down, read-
	// loop exit, or overflow). closed is the consumer side's exit
	// signal (Close), honored by both directions.
	frames    chan json.RawMessage
	closed    chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	downErr string
}

// OpenChat starts a chat tunnel session: mints the session id, installs
// the tunnel, and asks the extension to start its connector engine
// (chat_open). Refused while another session is live — two chat
// consumers on one extension race each other for every frame, so the
// second must lose loudly (the daemon-vs-TUI posture).
func (e *Extension) OpenChat() (*ChatTunnel, error) {
	t := &ChatTunnel{
		ext:    e,
		id:     newCorrelationID(),
		frames: make(chan json.RawMessage, chatFrameBuffer),
		closed: make(chan struct{}),
	}
	e.mu.Lock()
	if !e.connectorRole {
		e.mu.Unlock()
		return nil, errors.New("connector role not registered")
	}
	if e.chatTun != nil {
		e.mu.Unlock()
		return nil, errors.New("a chat session is already open on this extension")
	}
	e.chatTun = t
	e.mu.Unlock()

	if err := e.writeFrame(extproto.ChatOpenFromHost{Type: "chat_open", ID: t.id}); err != nil {
		e.mu.Lock()
		if e.chatTun == t {
			e.chatTun = nil
		}
		e.mu.Unlock()
		return nil, fmt.Errorf("write chat_open: %w", err)
	}
	return t, nil
}

// ReadFrame blocks for the next inner frame. It returns io.EOF after
// Close (the consumer left) or when the session ended without a reason
// (process exit, orderly chat_down), and a reason-carrying error when
// the extension reported its connector engine dead.
func (t *ChatTunnel) ReadFrame() ([]byte, error) {
	select {
	case f, ok := <-t.frames:
		if !ok {
			if reason := t.DownReason(); reason != "" {
				return nil, errors.New(reason)
			}
			return nil, io.EOF
		}
		return f, nil
	case <-t.closed:
		return nil, io.EOF
	}
}

// WriteFrame wraps one inner frame in a chat envelope and sends it.
func (t *ChatTunnel) WriteFrame(b []byte) error {
	select {
	case <-t.closed:
		return errors.New("chat session closed")
	default:
	}
	return t.ext.writeFrame(extproto.ChatFrame{Type: "chat", ID: t.id, Frame: json.RawMessage(b)})
}

// Close ends the session from the host side: detaches the tunnel and
// tells the extension to tear down its connector engine (chat_close).
// The extension process — and its tools — live on; a later OpenChat
// starts a fresh session. Safe to call more than once.
func (t *ChatTunnel) Close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		t.ext.mu.Lock()
		if t.ext.chatTun == t {
			t.ext.chatTun = nil
		}
		t.ext.mu.Unlock()
		_ = t.ext.writeFrame(extproto.ChatCloseFromHost{Type: "chat_close", ID: t.id})
	})
}

// DownReason reports why the extension ended the session: the chat_down
// reason, or "" (orderly teardown or process exit).
func (t *ChatTunnel) DownReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.downErr
}

// --- read-loop half (all called from the read-loop goroutine only) -----

// deliverChatFrame routes one inner frame to the live tunnel. Frames for
// a stale or unknown session are dropped with a log — after a
// close/reopen the old session's stragglers must not corrupt the new
// one. Never blocks: past the buffer the session is already broken
// (frames would be missing), so overflow ends it loudly.
func (e *Extension) deliverChatFrame(id string, frame json.RawMessage) {
	e.mu.Lock()
	t := e.chatTun
	e.mu.Unlock()
	if t == nil || t.id != id {
		fmt.Fprintf(e.logFile, "[terva] chat frame for stale session %q; dropped\n", id)
		return
	}
	select {
	case t.frames <- frame:
	default:
		fmt.Fprintf(e.logFile, "[terva] chat tunnel overflow; ending the session\n")
		e.closeChatTunnel(t, "chat tunnel overflow (consumer wedged)")
		_ = e.writeFrame(extproto.ChatCloseFromHost{Type: "chat_close", ID: t.id})
	}
}

// deliverChatDown ends the live tunnel with the extension's reason (its
// connector engine exited; "" means an orderly teardown).
func (e *Extension) deliverChatDown(id, reason string) {
	e.mu.Lock()
	t := e.chatTun
	e.mu.Unlock()
	if t == nil || t.id != id {
		fmt.Fprintf(e.logFile, "[terva] chat_down for stale session %q; dropped\n", id)
		return
	}
	e.closeChatTunnel(t, reason)
}

// closeChatOnExit ends a live tunnel when the read loop exits, so a
// chat consumer learns the process died instead of waiting forever.
func (e *Extension) closeChatOnExit() {
	e.mu.Lock()
	t := e.chatTun
	e.mu.Unlock()
	if t != nil {
		e.closeChatTunnel(t, "")
	}
}

// closeChatTunnel detaches the tunnel and closes its stream. Only the
// detacher closes the frames channel, so the close happens exactly once
// even when chat_down, read-loop exit, and a consumer Close race.
func (e *Extension) closeChatTunnel(t *ChatTunnel, reason string) {
	e.mu.Lock()
	if e.chatTun != t {
		e.mu.Unlock()
		return // already detached (consumer Close or an earlier end)
	}
	e.chatTun = nil
	e.mu.Unlock()
	t.mu.Lock()
	t.downErr = reason
	t.mu.Unlock()
	close(t.frames)
}
