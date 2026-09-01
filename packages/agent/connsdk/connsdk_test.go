package connsdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/testsupport"
)

type stubTransport struct {
	connectErr error
	receiveErr error // when set, Receive dies with it after the first delivery
	sent       chan Outgoing
	typing     chan string // "on:<chat>" / "off:<chat>", when non-nil
}

func (s *stubTransport) Connect(ctx context.Context) (Identity, error) {
	if s.connectErr != nil {
		return Identity{}, s.connectErr
	}
	return Identity{ID: "1", Username: "stub"}, nil
}

func (s *stubTransport) Receive(ctx context.Context, deliver func(Message)) error {
	deliver(Message{ChatID: "c", UserID: "u", Text: "inbound"})
	if s.receiveErr != nil {
		return s.receiveErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *stubTransport) Send(ctx context.Context, out Outgoing) error {
	if strings.Contains(out.Text, "fail") {
		return errors.New("send refused")
	}
	s.sent <- out
	return nil
}

func (s *stubTransport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (s *stubTransport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (s *stubTransport) Typing(ctx context.Context, chatID string) error {
	if s.typing != nil {
		s.typing <- "on:" + chatID
	}
	return nil
}

func (s *stubTransport) StopTyping(ctx context.Context, chatID string) error {
	if s.typing != nil {
		s.typing <- "off:" + chatID
	}
	return nil
}

// harness wires Serve() to in-memory pipes and provides frame-level
// helpers, playing the host's role.
type harness struct {
	t      *testing.T
	toSDK  io.WriteCloser
	frames *bufio.Scanner
	done   chan error
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	hostIn, sdkOut := io.Pipe() // sdk stdout -> host
	sdkIn, hostOut := io.Pipe() // host -> sdk stdin
	h := &harness{t: t, toSDK: hostOut, frames: bufio.NewScanner(hostIn), done: make(chan error, 1)}
	go func() {
		h.done <- Serve(cfg, sdkIn, sdkOut, io.Discard)
		sdkOut.Close()
	}()
	return h
}

func (h *harness) send(v any) {
	h.t.Helper()
	b, err := connproto.Encode(v)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.toSDK.Write(b); err != nil {
		h.t.Fatal(err)
	}
}

// next returns the next frame of the given type, failing on anything
// unexpected except interleaved warns/messages when skipOthers.
func (h *harness) next(wantType string) map[string]any {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	got := make(chan map[string]any, 1)
	go func() {
		for h.frames.Scan() {
			var m map[string]any
			if json.Unmarshal(h.frames.Bytes(), &m) != nil {
				continue
			}
			if m["type"] == wantType {
				got <- m
				return
			}
		}
		got <- nil
	}()
	select {
	case m := <-got:
		if m == nil {
			h.t.Fatalf("stream ended before a %q frame", wantType)
		}
		return m
	case <-deadline:
		h.t.Fatalf("timed out waiting for a %q frame", wantType)
		return nil
	}
}

func (h *harness) serveErr() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("serve did not return")
		return nil
	}
}

func testConfig(tr Transport) Config {
	return Config{
		Name:         "stub",
		Version:      "0.1",
		Capabilities: Capabilities{MaxTextLen: 100, TypingRefresh: time.Second, SendsImages: true},
		NewTransport: func(Session) (Transport, error) { return tr, nil },
	}
}

// typing{active:false} reaches the transport's StopTyping and never
// its Typing: a stop read as a start would lengthen the very tail the
// frame exists to cut.
func TestTypingStopReachesTheStopper(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 4), typing: make(chan string, 4)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2, DataDir: testsupport.TempDir(t)})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	off := false
	h.send(connproto.TypingFromHost{Type: "typing", ChatID: "c"})
	h.send(connproto.TypingFromHost{Type: "typing", ChatID: "c", Active: &off})
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-tr.typing:
			got[ev] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("typing events after 5s: %v, want on:c and off:c", got)
		}
	}
	if !got["on:c"] || !got["off:c"] {
		t.Errorf("typing events = %v, want exactly on:c and off:c", got)
	}
	select {
	case ev := <-tr.typing:
		t.Errorf("unexpected extra typing event %q", ev)
	case <-time.After(50 * time.Millisecond):
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

func TestServeHappyPath(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 4)}
	h := newHarness(t, testConfig(tr))

	hello := h.next("hello")
	if hello["name"] != "stub" {
		t.Errorf("hello = %v", hello)
	}
	caps, _ := hello["capabilities"].(map[string]any)
	if caps == nil || caps["max_text_len"] != float64(100) || caps["typing_refresh_ms"] != float64(1000) {
		t.Errorf("capabilities = %v", caps)
	}

	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1, DataDir: testsupport.TempDir(t)})

	// A send before connect fails politely instead of crashing.
	h.send(connproto.SendFromHost{Type: "send", ID: "0", ChatID: "c", Text: "early"})
	if res := h.next("result"); res["id"] != "0" || res["error"] != "not connected" {
		t.Errorf("pre-connect result = %v", res)
	}

	h.send(connproto.ConnectFromHost{Type: "connect"})
	if conn := h.next("connected"); conn["username"] != "stub" {
		t.Errorf("connected = %v", conn)
	}
	if msg := h.next("message"); msg["text"] != "inbound" {
		t.Errorf("message = %v", msg)
	}

	h.send(connproto.SendFromHost{Type: "send", ID: "1", ChatID: "c", Text: "hi"})
	if res := h.next("result"); res["id"] != "1" || res["error"] != nil {
		t.Errorf("result = %v", res)
	}
	if out := <-tr.sent; out.Text != "hi" || out.ChatID != "c" {
		t.Errorf("transport got %+v", out)
	}

	h.send(connproto.SendFromHost{Type: "send", ID: "2", ChatID: "c", Text: "fail please"})
	if res := h.next("result"); res["error"] != "send refused" {
		t.Errorf("error result = %v", res)
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	if err := h.serveErr(); err != nil {
		t.Errorf("serve returned %v on shutdown, want nil", err)
	}
}

func TestServeConnectError(t *testing.T) {
	tr := &stubTransport{connectErr: errors.New("bad token"), sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	if ce := h.next("connect_error"); ce["error"] != "bad token" {
		t.Errorf("connect_error = %v", ce)
	}
	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// idStubTransport upgrades the stub with protocol-2 identity: inbound
// messages carry ids, and sends return the created message's id
// (connsdk.MessageIDSender).
type idStubTransport struct {
	stubTransport
}

func (s *idStubTransport) Receive(ctx context.Context, deliver func(Message)) error {
	deliver(Message{ID: "m-77", TS: 1751469000123, ChatID: "c", ChatKind: "group",
		ChatTitle: "ops", UserID: "u", ReplyTo: "m-70", Text: "inbound"})
	<-ctx.Done()
	return ctx.Err()
}

func (s *idStubTransport) SendWithID(ctx context.Context, out Outgoing) (string, error) {
	if err := s.Send(ctx, out); err != nil {
		return "", err
	}
	return "m-99", nil
}

// TestServeProtocol2 drives the stage-A surface: the hello advertises
// [1,2]; a protocol-2 ack flows into Session; inbound identity fields
// ride the message frame; and SendWithID fills result.message_id.
func TestServeProtocol2(t *testing.T) {
	tr := &idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}}
	var gotSession Session
	h := newHarness(t, Config{
		Name: "stub", Version: "0.1",
		Capabilities: Capabilities{MaxTextLen: 100, Features: []string{"message_ids", "chat_kinds"}},
		NewTransport: func(s Session) (Transport, error) { gotSession = s; return tr, nil },
	})

	hello := h.next("hello")
	if hello["protocol_max"] != float64(connproto.ProtocolMax) {
		t.Errorf("hello protocol_max = %v, want %d", hello["protocol_max"], connproto.ProtocolMax)
	}
	caps, _ := hello["capabilities"].(map[string]any)
	if caps == nil || caps["features"] == nil {
		t.Errorf("hello capabilities missing features: %v", caps)
	}

	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2,
		Capabilities: &connproto.Capabilities{Features: []string{"message_ids"}}})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	msg := h.next("message")
	if msg["id"] != "m-77" || msg["ts"] != float64(1751469000123) ||
		msg["chat_kind"] != "group" || msg["reply_to"] != "m-70" {
		t.Errorf("v2 message frame = %v", msg)
	}
	if gotSession.Protocol != 2 || len(gotSession.HostFeatures) != 1 {
		t.Errorf("session = %+v, want protocol 2 + host features", gotSession)
	}

	h.send(connproto.SendFromHost{Type: "send", ID: "s1", ChatID: "c", Text: "hi"})
	if res := h.next("result"); res["id"] != "s1" || res["message_id"] != "m-99" {
		t.Errorf("result = %v, want message_id from SendWithID", res)
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// TestServeProtocol1Downgrade: against an old host the SDK emits the
// v1 shape — the message's OWN id rides reply_to, v2 fields stay home.
func TestServeProtocol1Downgrade(t *testing.T) {
	tr := &idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	msg := h.next("message")
	if msg["reply_to"] != "m-77" {
		t.Errorf("v1 message reply_to = %v, want the message's own id", msg["reply_to"])
	}
	if msg["id"] != nil || msg["chat_kind"] != nil || msg["ts"] != nil {
		t.Errorf("v1 message leaked v2 fields: %v", msg)
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

func TestServeRefusesProtocolMismatch(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 99})
	err := h.serveErr()
	if err == nil || !strings.Contains(err.Error(), "protocol 99") {
		t.Errorf("serve err = %v, want protocol refusal", err)
	}
}

// TestServeReceiveDeathExitsPromptly: a fatal transport failure ends
// Serve on its own — the serve loop must not sit blocked on host input
// while its transport is dead. The prompt exit is what triggers the
// host's restart budget (standalone) or chat_down (extension tunnel).
func TestServeReceiveDeathExitsPromptly(t *testing.T) {
	tr := &stubTransport{receiveErr: errors.New("gateway lost"), sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")
	// No further host frames: Serve must still return, with the warn
	// on the wire first.
	if w := h.next("warn"); !strings.Contains(w["message"].(string), "gateway lost") {
		t.Errorf("warn = %v", w)
	}
	err := h.serveErr()
	if err == nil || !strings.Contains(err.Error(), "gateway lost") {
		t.Errorf("serve err = %v, want the transport's receive error", err)
	}
}

func TestServeStdinClose(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1})
	h.toSDK.Close() // host vanished
	if err := h.serveErr(); err != nil {
		t.Errorf("serve err on stdin close = %v, want nil", err)
	}
}

func TestServeRequiresNewTransport(t *testing.T) {
	err := Serve(Config{Name: "x"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "NewTransport") {
		t.Errorf("err = %v", err)
	}
}

// askStubTransport implements Asker on top of the protocol-2 stub:
// Ask records the question and hands the deliver func to the test,
// which plays the human; CloseAsk records the withdrawal.
type askStubTransport struct {
	idStubTransport
	asks    chan Ask
	deliver func(Answer) // captured by Ask; the test drives it
	closes  chan [2]string
}

func (s *askStubTransport) Ask(ctx context.Context, a Ask, deliver func(Answer)) (string, error) {
	s.deliver = deliver
	s.asks <- a
	return "m-ask-1", nil
}

func (s *askStubTransport) CloseAsk(ctx context.Context, askID, outcome string) error {
	s.closes <- [2]string{askID, outcome}
	return nil
}

// TestServeAsk drives the stage-G author surface: the ask frame maps
// onto Asker.Ask (result carries the rendered message id), answers
// flow back as answer frames with the ask id pinned and restrict_to
// re-filtered SDK-side, and ask_close routes to CloseAsk.
func TestServeAsk(t *testing.T) {
	tr := &askStubTransport{
		idStubTransport: idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}},
		asks:            make(chan Ask, 1),
		closes:          make(chan [2]string, 1),
	}
	cfg := testConfig(tr)
	cfg.Capabilities.Features = []string{"message_ids", "asks"}
	h := newHarness(t, cfg)
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2, DataDir: testsupport.TempDir(t)})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	h.send(connproto.AskFromHost{Type: "ask", ID: "a1", ChatID: "c1", ReplyTo: "m12",
		Text: "approve?", Options: []connproto.AskOption{
			{Key: "approve", Label: "Approve", Style: "affirm"},
			{Key: "deny", Label: "Deny", Style: "deny"},
		}, RestrictTo: []string{"u1"}, ExpiresMS: 120000})

	var a Ask
	select {
	case a = <-tr.asks:
	case <-time.After(3 * time.Second):
		t.Fatal("Asker.Ask never called")
	}
	if a.ID != "a1" || a.ChatID != "c1" || len(a.Options) != 2 || a.Options[1].Style != "deny" ||
		a.Expires != 2*time.Minute || len(a.RestrictTo) != 1 {
		t.Errorf("ask = %+v", a)
	}
	if res := h.next("result"); res["id"] != "a1" || res["message_id"] != "m-ask-1" {
		t.Errorf("ask result = %v", res)
	}

	// A disallowed responder is dropped before the wire; the allowed
	// one becomes an answer frame with the ask id pinned. Deliver off
	// the test goroutine — the frame write blocks on the unbuffered
	// pipe until next() below starts reading.
	go func() {
		tr.deliver(Answer{AskID: "a1", Key: "approve", UserID: "intruder"})
		tr.deliver(Answer{AskID: "spoofed", Key: "approve", UserID: "u1", Username: "drew", Attestation: AttestationAttested})
	}()
	ans := h.next("answer")
	if ans["ask_id"] != "a1" || ans["key"] != "approve" || ans["user_id"] != "u1" || ans["attestation"] != "attested" {
		t.Errorf("answer = %v", ans)
	}

	h.send(connproto.AskCloseFromHost{Type: "ask_close", ID: "a2", AskID: "a1", Outcome: "Approve — @drew"})
	select {
	case cl := <-tr.closes:
		if cl[0] != "a1" || cl[1] != "Approve — @drew" {
			t.Errorf("CloseAsk = %v", cl)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CloseAsk never called")
	}
	if res := h.next("result"); res["id"] != "a2" || res["error"] != nil {
		t.Errorf("ask_close result = %v", res)
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// TestServeAskUnsupported: a host that sends ask to a transport
// without Asker gets a clean result error, not a crash. (A correct
// host never does — the feature was not declared — but the SDK must
// stay calm about protocol-shaped mistakes.)
func TestServeAskUnsupported(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")
	h.send(connproto.AskFromHost{Type: "ask", ID: "a1", ChatID: "c1", Text: "?",
		Options: []connproto.AskOption{{Key: "ok", Label: "OK"}}})
	if res := h.next("result"); res["id"] != "a1" || res["error"] != "connector does not support asks" {
		t.Errorf("result = %v", res)
	}
	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// speakerStubTransport implements SpeakerSender on the protocol-2
// stub: speaker sends are recorded separately from plain ones.
type speakerStubTransport struct {
	idStubTransport
	spoken chan Outgoing
}

func (s *speakerStubTransport) SendAsSpeaker(ctx context.Context, out Outgoing) (string, error) {
	s.spoken <- out
	return "m-cast-1", nil
}

// TestServeSendSpeaker: a speaker frame routes to SendAsSpeaker (with
// the id in the result); a plain send still takes the ordinary path.
func TestServeSendSpeaker(t *testing.T) {
	tr := &speakerStubTransport{
		idStubTransport: idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}},
		spoken:          make(chan Outgoing, 4),
	}
	cfg := testConfig(tr)
	cfg.Capabilities.Features = []string{"message_ids", "speaker:name_only"}
	h := newHarness(t, cfg)
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	h.send(connproto.SendFromHost{Type: "send", ID: "s1", ChatID: "c1", Text: "The airlock hisses open.",
		Speaker: &connproto.Speaker{Key: "kaiku", Name: "Kaiku"}})
	if res := h.next("result"); res["id"] != "s1" || res["message_id"] != "m-cast-1" || res["error"] != nil {
		t.Errorf("speaker result = %v", res)
	}
	select {
	case out := <-tr.spoken:
		if out.Speaker == nil || out.Speaker.Key != "kaiku" || out.Text != "The airlock hisses open." {
			t.Errorf("speaker send = %+v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendAsSpeaker never called")
	}

	// A plain send is untouched by the speaker machinery.
	h.send(connproto.SendFromHost{Type: "send", ID: "s2", ChatID: "c1", Text: "plain"})
	if res := h.next("result"); res["id"] != "s2" || res["message_id"] != "m-99" {
		t.Errorf("plain result = %v", res)
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// TestServeSendSpeakerDowngrade: a transport without SpeakerSender
// still delivers — the SDK renders the same prefix fallback the host
// uses, so a host/author feature mismatch degrades instead of failing.
func TestServeSendSpeakerDowngrade(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 4)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	h.send(connproto.SendFromHost{Type: "send", ID: "s1", ChatID: "c1", Text: "hello",
		Speaker: &connproto.Speaker{Key: "aava", Name: "Aava"}})
	if res := h.next("result"); res["id"] != "s1" || res["error"] != nil {
		t.Errorf("result = %v", res)
	}
	select {
	case out := <-tr.sent:
		if out.Text != "**Aava:** hello" || out.Speaker != nil {
			t.Errorf("downgraded send = %+v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send never reached the transport")
	}
	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// threadStubTransport adds the connsdk.Threader upgrade.
type threadStubTransport struct {
	idStubTransport
	threads chan [3]string
}

func (s *threadStubTransport) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	s.threads <- [3]string{chatID, fromMessageID, name}
	return "t-99", nil
}

// TestServeThreadStart: the frame maps onto Threader.StartThread and
// the result carries the new chat id; a transport without the
// interface gets a clean result error.
func TestServeThreadStart(t *testing.T) {
	tr := &threadStubTransport{
		idStubTransport: idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}},
		threads:         make(chan [3]string, 1),
	}
	cfg := testConfig(tr)
	cfg.Capabilities.Features = []string{"threads_out"}
	h := newHarness(t, cfg)
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	h.send(connproto.ThreadStartFromHost{Type: "thread_start", ID: "t1", ChatID: "c1",
		FromMessageID: "m-12", Name: "sidebar"})
	if res := h.next("result"); res["id"] != "t1" || res["chat_id"] != "t-99" || res["error"] != nil {
		t.Errorf("thread result = %v", res)
	}
	select {
	case got := <-tr.threads:
		if got != [3]string{"c1", "m-12", "sidebar"} {
			t.Errorf("StartThread args = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartThread never called")
	}

	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

func TestServeThreadStartUnsupported(t *testing.T) {
	tr := &stubTransport{sent: make(chan Outgoing, 1)}
	h := newHarness(t, testConfig(tr))
	h.next("hello")
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")
	h.send(connproto.ThreadStartFromHost{Type: "thread_start", ID: "t1", ChatID: "c1", Name: "x"})
	if res := h.next("result"); res["id"] != "t1" || res["error"] != "connector does not support threads" {
		t.Errorf("result = %v", res)
	}
	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()
}

// eventStubTransport adds the stage-D upgrades: an event source the
// test drives plus the three outbound command surfaces.
type eventStubTransport struct {
	idStubTransport
	sink  chan ChatEventSink // captured at ReceiveChatEvents
	calls chan string
}

func (s *eventStubTransport) ReceiveChatEvents(ctx context.Context, sink ChatEventSink) error {
	s.sink <- sink
	<-ctx.Done()
	return nil
}

func (s *eventStubTransport) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	s.calls <- "edit|" + chatID + "|" + messageID + "|" + text
	return nil
}

func (s *eventStubTransport) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	s.calls <- fmt.Sprintf("react|%s|%s|%s|%v", chatID, messageID, key, remove)
	return nil
}

func (s *eventStubTransport) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	s.calls <- "delete|" + chatID + "|" + messageID
	return nil
}

// TestServeChatEvents: inbound events become frames; outbound
// edit/react/delete route to the transport with results.
func TestServeChatEvents(t *testing.T) {
	tr := &eventStubTransport{
		idStubTransport: idStubTransport{stubTransport{sent: make(chan Outgoing, 4)}},
		sink:            make(chan ChatEventSink, 1),
		calls:           make(chan string, 4),
	}
	cfg := testConfig(tr)
	cfg.Capabilities.Features = []string{"edits_in", "reactions_in", "edits_out", "reactions_out", "deletes_out"}
	cfg.Capabilities.MinEditInterval = time.Second
	h := newHarness(t, cfg)
	hello := h.next("hello")
	caps, _ := hello["capabilities"].(map[string]any)
	if caps["min_edit_interval_ms"] != float64(1000) {
		t.Errorf("hello min_edit_interval_ms = %v", caps["min_edit_interval_ms"])
	}
	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h.send(connproto.ConnectFromHost{Type: "connect"})
	h.next("connected")

	var sink ChatEventSink
	select {
	case sink = <-tr.sink:
	case <-time.After(3 * time.Second):
		t.Fatal("ReceiveChatEvents never called")
	}
	go sink.Edited(MessageEdited{ChatID: "c1", ID: "m10", Text: "fixed"})
	if ev := h.next("message_edited"); ev["chat_id"] != "c1" || ev["id"] != "m10" || ev["text"] != "fixed" {
		t.Errorf("message_edited = %v", ev)
	}
	go sink.Reaction(Reaction{ChatID: "c1", MessageID: "m-90", UserID: "u1", Key: "👍", Removed: true})
	if ev := h.next("reaction"); ev["message_id"] != "m-90" || ev["removed"] != true {
		t.Errorf("reaction = %v", ev)
	}

	h.send(connproto.EditFromHost{Type: "edit", ID: "e1", ChatID: "c1", MessageID: "m-90", Text: "v2"})
	if res := h.next("result"); res["id"] != "e1" || res["error"] != nil {
		t.Errorf("edit result = %v", res)
	}
	h.send(connproto.ReactFromHost{Type: "react", ID: "r1", ChatID: "c1", MessageID: "m-12", Key: "👀"})
	if res := h.next("result"); res["id"] != "r1" || res["error"] != nil {
		t.Errorf("react result = %v", res)
	}
	h.send(connproto.DeleteFromHost{Type: "delete", ID: "d1", ChatID: "c1", MessageID: "m-90"})
	if res := h.next("result"); res["id"] != "d1" || res["error"] != nil {
		t.Errorf("delete result = %v", res)
	}
	want := map[string]bool{
		"edit|c1|m-90|v2": false, "react|c1|m-12|👀|false": false, "delete|c1|m-90": false,
	}
	for range want {
		select {
		case c := <-tr.calls:
			if _, ok := want[c]; !ok {
				t.Errorf("unexpected call %q", c)
			}
			want[c] = true
		case <-time.After(2 * time.Second):
			t.Fatal("transport call missing")
		}
	}

	// A transport without the interfaces answers with clean errors.
	h.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h.serveErr()

	tr2 := &stubTransport{sent: make(chan Outgoing, 1)}
	h2 := newHarness(t, testConfig(tr2))
	h2.next("hello")
	h2.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 2})
	h2.send(connproto.ConnectFromHost{Type: "connect"})
	h2.next("connected")
	h2.send(connproto.EditFromHost{Type: "edit", ID: "e9", ChatID: "c", MessageID: "m", Text: "x"})
	if res := h2.next("result"); res["error"] != "connector does not support edits" {
		t.Errorf("ungated edit result = %v", res)
	}
	h2.send(connproto.ShutdownFromHost{Type: "shutdown"})
	_ = h2.serveErr()
}
