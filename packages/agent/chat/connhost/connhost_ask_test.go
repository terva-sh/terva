package connhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/testsupport"
)

// scriptConn is a FrameConn the test drives by hand: frames pushed
// into inbound arrive at the session; frames the session writes land
// on writes.
type scriptConn struct {
	inbound chan []byte
	writes  chan []byte

	mu     sync.Mutex
	closed bool
}

func newScriptConn() *scriptConn {
	return &scriptConn{inbound: make(chan []byte, 16), writes: make(chan []byte, 16)}
}

func (c *scriptConn) ReadFrame() ([]byte, error) {
	b, ok := <-c.inbound
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}

func (c *scriptConn) WriteFrame(b []byte) error {
	c.writes <- append([]byte(nil), b...)
	return nil
}

func (c *scriptConn) push(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c.inbound <- b
}

// next reads the session's next written frame and requires its type.
func (c *scriptConn) next(t *testing.T, wantType string) []byte {
	t.Helper()
	select {
	case b := <-c.writes:
		var f connproto.Frame
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("unmarshal written frame: %v", err)
		}
		if f.Type != wantType {
			t.Fatalf("session wrote %q, want %q (frame: %s)", f.Type, wantType, b)
		}
		return b
	case <-time.After(3 * time.Second):
		t.Fatalf("session never wrote a %q frame", wantType)
		return nil
	}
}

// startAskSession runs the handshake for a protocol-2 connector whose
// features are given, returning the live session and the script conn.
func startAskSession(t *testing.T, features []string) (*Session, *scriptConn) {
	t.Helper()
	sc := newScriptConn()
	s := New(Config{
		Name: "stub", DataDir: testsupport.TempDir(t),
		Conn:        sc,
		Deliver:     func(chat.Message) {},
		Warn:        func(string) {},
		Log:         func(string) {},
		SendTimeout: 2 * time.Second,
	})
	sc.push(t, connproto.HelloFromConn{
		Type: "hello", Name: "stub", ProtocolMin: 1, ProtocolMax: 2,
		Capabilities: connproto.Capabilities{Features: features},
	})
	if err := s.Start(time.Second); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sc.next(t, "hello_ack")
	t.Cleanup(func() { close(sc.inbound) })
	return s, sc
}

// TestSessionAsk drives the full happy path: render acknowledged, an
// unauthorized answer filtered host-side, the first valid answer wins,
// and the close carries the label+user outcome.
func TestSessionAsk(t *testing.T) {
	s, sc := startAskSession(t, []string{"asks"})
	if !s.Capabilities().Asks {
		t.Fatal("feature 'asks' should set Capabilities.Asks")
	}

	type result struct {
		ans chat.Answer
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := s.Ask(context.Background(), chat.Ask{
			ChatID: "c1", ReplyTo: "m12", Text: "approve?",
			Options: []chat.AskOption{
				{Key: "approve", Label: "Approve", Style: "affirm"},
				{Key: "deny", Label: "Deny", Style: "deny"},
			},
			RestrictTo: []string{"u1"},
			Timeout:    5 * time.Second,
		})
		done <- result{ans, err}
	}()

	raw := sc.next(t, "ask")
	var ask connproto.AskFromHost
	if err := json.Unmarshal(raw, &ask); err != nil {
		t.Fatalf("decode ask: %v", err)
	}
	if ask.ChatID != "c1" || ask.ReplyTo != "m12" || len(ask.Options) != 2 || ask.Options[0].Key != "approve" {
		t.Errorf("ask frame = %+v", ask)
	}
	if len(ask.RestrictTo) != 1 || ask.RestrictTo[0] != "u1" || ask.ExpiresMS != 5000 {
		t.Errorf("ask restrict/expiry = %+v", ask)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: ask.ID, MessageID: "m-90"})

	// The host re-filters restrict_to: an intruder's answer must not win.
	sc.push(t, connproto.AnswerFromConn{Type: "answer", AskID: ask.ID, Key: "approve", UserID: "intruder"})
	sc.push(t, connproto.AnswerFromConn{Type: "answer", AskID: ask.ID, Key: "approve",
		UserID: "u1", Username: "drew", Attestation: connproto.AttestationAttested})

	rawClose := sc.next(t, "ask_close")
	var cl connproto.AskCloseFromHost
	if err := json.Unmarshal(rawClose, &cl); err != nil {
		t.Fatalf("decode ask_close: %v", err)
	}
	if cl.AskID != ask.ID || cl.Outcome != "Approve — @drew" {
		t.Errorf("ask_close = %+v", cl)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: cl.ID})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Ask: %v", r.err)
		}
		// DeepEqual, not ==: chat.Answer carries a Keys slice since
		// multi-select landed, so the struct is no longer comparable.
		want := chat.Answer{Key: "approve", UserID: "u1", Username: "drew", Attestation: chat.AttestationAttested}
		if !reflect.DeepEqual(r.ans, want) {
			t.Errorf("answer = %+v, want %+v", r.ans, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask never returned")
	}
}

// TestSessionAskTimeout: nobody answers — the ask is withdrawn with
// the fail-closed outcome and ErrAskTimeout comes back.
func TestSessionAskTimeout(t *testing.T) {
	s, sc := startAskSession(t, []string{"asks"})

	done := make(chan error, 1)
	go func() {
		_, err := s.Ask(context.Background(), chat.Ask{
			ChatID: "c1", Text: "anyone?",
			Options:        []chat.AskOption{{Key: "ok", Label: "OK"}},
			Timeout:        80 * time.Millisecond,
			TimeoutOutcome: "no answer — denied",
		})
		done <- err
	}()

	raw := sc.next(t, "ask")
	var ask connproto.AskFromHost
	_ = json.Unmarshal(raw, &ask)
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: ask.ID})

	rawClose := sc.next(t, "ask_close")
	var cl connproto.AskCloseFromHost
	_ = json.Unmarshal(rawClose, &cl)
	if cl.AskID != ask.ID || cl.Outcome != "no answer — denied" {
		t.Errorf("ask_close = %+v", cl)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: cl.ID})

	select {
	case err := <-done:
		if !errors.Is(err, chat.ErrAskTimeout) {
			t.Errorf("Ask = %v, want ErrAskTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask never returned")
	}
}

// TestSessionAskUnsupported: a connector that never declared "asks"
// (or negotiated protocol 1) refuses immediately, writing nothing.
func TestSessionAskUnsupported(t *testing.T) {
	s, sc := startAskSession(t, []string{"message_ids"})
	if s.Capabilities().Asks {
		t.Fatal("Asks should be false without the feature")
	}
	_, err := s.Ask(context.Background(), chat.Ask{ChatID: "c1", Text: "?",
		Options: []chat.AskOption{{Key: "ok", Label: "OK"}}})
	if err == nil || !strings.Contains(err.Error(), "does not support asks") {
		t.Errorf("Ask = %v, want unsupported error", err)
	}
	select {
	case b := <-sc.writes:
		t.Errorf("unexpected frame written: %s", b)
	default:
	}
}

// TestSessionSendSpeaker: a speaker rides the frame for a graded
// connector and collapses to the prefix fallback for everyone else.
func TestSessionSendSpeaker(t *testing.T) {
	s, sc := startAskSession(t, []string{"speaker:name_only"})
	if got := s.Capabilities().Speaker; got != chat.SpeakerNameOnly {
		t.Fatalf("Speaker grade = %q, want name_only", got)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.Send(context.Background(), chat.Outgoing{
			ChatID: "c1", Text: "The airlock hisses open.",
			Speaker: &chat.Speaker{Key: "kaiku", Name: "Kaiku"},
		})
	}()
	raw := sc.next(t, "send")
	var f connproto.SendFromHost
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode send: %v", err)
	}
	if f.Speaker == nil || f.Speaker.Key != "kaiku" || f.Speaker.Name != "Kaiku" {
		t.Errorf("speaker = %+v", f.Speaker)
	}
	if f.Text != "The airlock hisses open." {
		t.Errorf("text = %q (must not be prefix-rewritten)", f.Text)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: f.ID})
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSessionSendSpeakerFallback(t *testing.T) {
	s, sc := startAskSession(t, []string{"message_ids"}) // no speaker grade
	done := make(chan error, 1)
	go func() {
		done <- s.Send(context.Background(), chat.Outgoing{
			ChatID: "c1", Text: "hello",
			Speaker: &chat.Speaker{Key: "aava", Name: "Aava"},
		})
	}()
	raw := sc.next(t, "send")
	var f connproto.SendFromHost
	_ = json.Unmarshal(raw, &f)
	if f.Speaker != nil {
		t.Errorf("speaker leaked to an ungraded connector: %+v", f.Speaker)
	}
	if f.Text != "**Aava:** hello" {
		t.Errorf("fallback text = %q", f.Text)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: f.ID})
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestSessionStartThread: the round trip returns the result's new
// thread chat id; connectors without the feature refuse locally.
func TestSessionStartThread(t *testing.T) {
	s, sc := startAskSession(t, []string{"threads_out"})
	if !s.Capabilities().ThreadsOut {
		t.Fatal("feature 'threads_out' should set Capabilities.ThreadsOut")
	}

	type result struct {
		id  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := s.StartThread(context.Background(), "c1", "m-12", "refactor: extract session core")
		done <- result{id, err}
	}()
	raw := sc.next(t, "thread_start")
	var f connproto.ThreadStartFromHost
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode thread_start: %v", err)
	}
	if f.ChatID != "c1" || f.FromMessageID != "m-12" || f.Name != "refactor: extract session core" {
		t.Errorf("thread_start = %+v", f)
	}
	sc.push(t, connproto.ResultFromConn{Type: "result", ID: f.ID, ChatID: "t-99"})
	select {
	case r := <-done:
		if r.err != nil || r.id != "t-99" {
			t.Errorf("StartThread = %q, %v", r.id, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StartThread never returned")
	}

	// Without the feature: refused before any frame is written.
	s2, sc2 := startAskSession(t, nil)
	if _, err := s2.StartThread(context.Background(), "c1", "", "x"); err == nil || !strings.Contains(err.Error(), "does not support threads") {
		t.Errorf("StartThread = %v, want unsupported error", err)
	}
	select {
	case b := <-sc2.writes:
		t.Errorf("unexpected frame written: %s", b)
	default:
	}
}
