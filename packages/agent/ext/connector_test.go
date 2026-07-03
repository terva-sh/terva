package ext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/extproto"
)

// stubTransport is a scriptable connsdk.Transport — the SAME interface a
// standalone connector implements, which is the point of the tunnel
// design. Inbound is injected via the deliver channel; every outbound
// call is recorded.
type stubTransport struct {
	mu         sync.Mutex
	connectErr error
	receiveErr error // returned by Receive after receiveDie closes
	receiveDie chan struct{}
	deliver    chan connsdk.Message
	sends      []connsdk.Outgoing
	session    connsdk.Session // what NewTransport was handed
	ctxDone    chan struct{}   // closed when Receive's ctx first ends
	ctxOnce    sync.Once       // Receive may run again after a reopen
}

func newStubTransport() *stubTransport {
	return &stubTransport{
		receiveDie: make(chan struct{}),
		deliver:    make(chan connsdk.Message, 8),
		ctxDone:    make(chan struct{}),
	}
}

func (s *stubTransport) newTransport(sess connsdk.Session) (connsdk.Transport, error) {
	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()
	return s, nil
}

func (s *stubTransport) Connect(ctx context.Context) (connsdk.Identity, error) {
	if s.connectErr != nil {
		return connsdk.Identity{}, s.connectErr
	}
	return connsdk.Identity{ID: "bot-1", Username: "stub"}, nil
}

func (s *stubTransport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	defer s.ctxOnce.Do(func() { close(s.ctxDone) })
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.receiveDie:
			return s.receiveErr
		case m := <-s.deliver:
			deliver(m)
		}
	}
}

func (s *stubTransport) Send(ctx context.Context, out connsdk.Outgoing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, out)
	if out.Text == "explode" {
		return errors.New("service said no")
	}
	return nil
}

func (s *stubTransport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return errors.New("no images")
}

func (s *stubTransport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return errors.New("no files")
}

func (s *stubTransport) Typing(ctx context.Context, chatID string) error { return nil }

// sendInner wraps one connproto frame in a chat envelope, host→ext.
func sendInner(t *testing.T, h *extHarness, sid string, inner any) {
	t.Helper()
	b, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	h.sendToExt(t, extproto.ChatFrame{Type: "chat", ID: sid, Frame: b})
}

// drainInner waits for the next chat envelope of session sid whose inner
// frame has the wanted type, skipping other sessions and inner types
// (warns, stray messages).
func drainInner(t *testing.T, h *extHarness, sid, want string) json.RawMessage {
	t.Helper()
	for {
		f := h.drainUntil(t, "chat")
		var cf extproto.ChatFrame
		if err := json.Unmarshal(f.raw, &cf); err != nil || cf.ID != sid {
			continue
		}
		var hdr struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(cf.Frame, &hdr); err == nil && hdr.Type == want {
			return cf.Frame
		}
	}
}

// drainChatDown waits for the session's chat_down and returns its error.
func drainChatDown(t *testing.T, h *extHarness, sid string) string {
	t.Helper()
	for {
		f := h.drainUntil(t, "chat_down")
		var cd extproto.ChatDownFromExt
		if err := json.Unmarshal(f.raw, &cd); err == nil && cd.ID == sid {
			return cd.Error
		}
	}
}

// connectorHarness builds a dual-role extension (one tool + the stub
// transport) and completes the extension handshake.
func connectorHarness(t *testing.T) (*extHarness, *stubTransport) {
	t.Helper()
	h := newHarness("duplex")
	st := newStubTransport()
	h.ext.Tool("echo", "echoes", json.RawMessage(`{"type":"object"}`), func(args json.RawMessage) ToolResult {
		return TextResult("echo:" + string(args))
	})
	h.ext.Connector(connsdk.Capabilities{MaxTextLen: 4096, TypingRefresh: 5 * time.Second, SendsImages: true}, st.newTransport)
	go h.ext.Run()
	h.handshake(t)
	return h, st
}

// openSession drives chat_open through the inner hello/hello_ack and
// connect/connected round trips — the full connproto handshake riding
// the tunnel — and returns once the session is live.
func openSession(t *testing.T, h *extHarness, sid string) {
	t.Helper()
	h.sendToExt(t, extproto.ChatOpenFromHost{Type: "chat_open", ID: sid})

	hello := drainInner(t, h, sid, "hello")
	var hf struct {
		ProtocolMin  int `json:"protocol_min"`
		ProtocolMax  int `json:"protocol_max"`
		Capabilities struct {
			MaxTextLen int `json:"max_text_len"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(hello, &hf); err != nil {
		t.Fatalf("inner hello: %v", err)
	}
	if hf.ProtocolMin > 1 || hf.ProtocolMax < 1 {
		t.Fatalf("inner hello protocol range %d..%d does not cover 1", hf.ProtocolMin, hf.ProtocolMax)
	}
	if hf.Capabilities.MaxTextLen != 4096 {
		t.Errorf("inner hello caps.max_text_len = %d, want 4096 (declared in Connector())", hf.Capabilities.MaxTextLen)
	}

	sendInner(t, h, sid, map[string]any{"type": "hello_ack", "protocol": 1, "data_dir": "/tmp/ext-data"})
	sendInner(t, h, sid, map[string]any{"type": "connect"})

	conn := drainInner(t, h, sid, "connected")
	var cf struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(conn, &cf); err != nil {
		t.Fatalf("inner connected: %v", err)
	}
	if cf.ID != "bot-1" || cf.Username != "stub" {
		t.Errorf("identity = %+v, want bot-1/stub", cf)
	}
}

// TestConnectorRegistersOnRun proves Run flushes the bare
// register_connector role declaration alongside the ordinary
// registrations — capabilities travel in the inner hello, not here.
func TestConnectorRegistersOnRun(t *testing.T) {
	h := newHarness("duplex")
	st := newStubTransport()
	h.ext.Connector(connsdk.Capabilities{MaxTextLen: 100}, st.newTransport)
	go h.ext.Run()

	f := h.next(t)
	if f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	seen := false
	for {
		f = h.next(t)
		if f.hdr.Type == "register_connector" {
			seen = true
		}
		if f.hdr.Type == "ready" {
			break
		}
	}
	if !seen {
		t.Fatal("register_connector never flushed before ready")
	}
}

// TestChatSessionFlow drives the full tunneled session: chat_open → the
// connsdk engine's inner hello → hello_ack → connect/connected with the
// transport's identity (and the host-assigned DataDir reaching the
// transport), inbound delivery as inner message frames, and inner sends
// round-tripping to the transport with result acks.
func TestChatSessionFlow(t *testing.T) {
	h, st := connectorHarness(t)
	openSession(t, h, "s1")

	st.mu.Lock()
	dataDir := st.session.DataDir
	st.mu.Unlock()
	if dataDir != "/tmp/ext-data" {
		t.Errorf("transport session DataDir = %q, want the inner hello_ack's", dataDir)
	}

	// Inbound: transport delivers → inner message frame in an envelope.
	st.deliver <- connsdk.Message{ChatID: "c1", UserID: "u1", Username: "drew", Text: "hi",
		Attachments: []connsdk.Attachment{{MimeType: "image/png", Path: "/data/x.png"}}}
	msg := drainInner(t, h, "s1", "message")
	var mf struct {
		ChatID      string `json:"chat_id"`
		UserID      string `json:"user_id"`
		Text        string `json:"text"`
		Attachments []struct {
			Path string `json:"path"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(msg, &mf); err != nil {
		t.Fatalf("inner message: %v", err)
	}
	if mf.ChatID != "c1" || mf.UserID != "u1" || mf.Text != "hi" {
		t.Errorf("message = %+v", mf)
	}
	if len(mf.Attachments) != 1 || mf.Attachments[0].Path != "/data/x.png" {
		t.Errorf("attachments = %+v", mf.Attachments)
	}

	// Outbound: inner send → transport.Send + ok result.
	sendInner(t, h, "s1", map[string]any{"type": "send", "id": "r1", "chat_id": "c1", "text": "hello"})
	res := drainInner(t, h, "s1", "result")
	var rf struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(res, &rf); err != nil {
		t.Fatalf("inner result: %v", err)
	}
	if rf.ID != "r1" || rf.Error != "" {
		t.Errorf("result = %+v, want ok for r1", rf)
	}
	st.mu.Lock()
	sends := len(st.sends)
	st.mu.Unlock()
	if sends != 1 {
		t.Errorf("transport got %d sends, want 1", sends)
	}

	// Outbound error: the transport's failure rides the result, not a crash.
	sendInner(t, h, "s1", map[string]any{"type": "send", "id": "r2", "chat_id": "c1", "text": "explode"})
	res = drainInner(t, h, "s1", "result")
	if err := json.Unmarshal(res, &rf); err != nil {
		t.Fatalf("inner result: %v", err)
	}
	if rf.ID != "r2" || rf.Error == "" {
		t.Errorf("result = %+v, want error for r2", rf)
	}
}

// TestToolCallServedWhileChatConnected is the reentrancy case: a turn
// started by this connector calls this same extension's tool mid-turn —
// the tool_call and the live chat session share one stdin and must not
// deadlock.
func TestToolCallServedWhileChatConnected(t *testing.T) {
	h, st := connectorHarness(t)
	openSession(t, h, "s1")
	st.deliver <- connsdk.Message{ChatID: "c1", UserID: "u1", Text: "use your tool"}
	drainInner(t, h, "s1", "message")

	h.sendToExt(t, extproto.ToolCallFromHost{Type: "tool_call", ID: "t1", Name: "echo", Args: json.RawMessage(`{"q":1}`)})
	f := h.drainUntil(t, "tool_result")
	var tr extproto.ToolResultFromExt
	if err := json.Unmarshal(f.raw, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.ID != "t1" || tr.IsError {
		t.Errorf("tool_result = %+v, want ok for t1", tr)
	}
}

// TestChatOpenWithoutConnector: an extension that never declared the
// role answers chat_open with an explanatory chat_down.
func TestChatOpenWithoutConnector(t *testing.T) {
	h := newHarness("plain")
	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.ChatOpenFromHost{Type: "chat_open", ID: "s1"})
	if msg := drainChatDown(t, h, "s1"); msg == "" {
		t.Error("chat_down should explain the missing connector")
	}
}

// TestChatConnectError maps a failed dial to the inner connect_error —
// in-band, session still alive (the host decides what to do).
func TestChatConnectError(t *testing.T) {
	h := newHarness("duplex")
	st := newStubTransport()
	st.connectErr = fmt.Errorf("bad token")
	h.ext.Connector(connsdk.Capabilities{}, st.newTransport)
	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.ChatOpenFromHost{Type: "chat_open", ID: "s1"})
	drainInner(t, h, "s1", "hello")
	sendInner(t, h, "s1", map[string]any{"type": "hello_ack", "protocol": 1})
	sendInner(t, h, "s1", map[string]any{"type": "connect"})
	ce := drainInner(t, h, "s1", "connect_error")
	var cf struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(ce, &cf); err != nil || cf.Error != "bad token" {
		t.Errorf("connect_error = %s (err=%v), want the transport's", ce, err)
	}
}

// TestReceiveDeathReportsChatDown: a fatal transport failure must end
// the session with a reason-carrying chat_down — while the process, and
// its tools, live on.
func TestReceiveDeathReportsChatDown(t *testing.T) {
	h, st := connectorHarness(t)
	openSession(t, h, "s1")

	st.receiveErr = errors.New("auth revoked")
	close(st.receiveDie)

	if msg := drainChatDown(t, h, "s1"); msg != "auth revoked" {
		t.Errorf("chat_down error = %q, want the transport's", msg)
	}

	// The extension half is untouched: tools still serve.
	h.sendToExt(t, extproto.ToolCallFromHost{Type: "tool_call", ID: "t1", Name: "echo", Args: json.RawMessage(`{}`)})
	f := h.drainUntil(t, "tool_result")
	var tr extproto.ToolResultFromExt
	if err := json.Unmarshal(f.raw, &tr); err != nil || tr.IsError {
		t.Errorf("tool after chat_down: %+v err=%v", tr, err)
	}
}

// TestChatCloseStopsReceive: a host-initiated close cancels the
// transport's ctx, confirms with a clean chat_down, and a later
// chat_open starts a fresh session (fresh engine, fresh handshake).
func TestChatCloseStopsReceive(t *testing.T) {
	h, st := connectorHarness(t)
	openSession(t, h, "s1")

	h.sendToExt(t, extproto.ChatCloseFromHost{Type: "chat_close", ID: "s1"})
	select {
	case <-st.ctxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("transport ctx never cancelled on chat_close")
	}
	if msg := drainChatDown(t, h, "s1"); msg != "" {
		t.Errorf("chat_down after orderly close carries %q, want clean", msg)
	}

	// Reopen works: a whole new session, hello onward.
	openSession(t, h, "s2")
}
