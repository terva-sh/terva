package connsdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/connproto"
)

type stubTransport struct {
	connectErr error
	sent       chan Outgoing
}

func (s *stubTransport) Connect(ctx context.Context) (Identity, error) {
	if s.connectErr != nil {
		return Identity{}, s.connectErr
	}
	return Identity{ID: "1", Username: "stub"}, nil
}

func (s *stubTransport) Receive(ctx context.Context, deliver func(Message)) error {
	deliver(Message{ChatID: "c", UserID: "u", Text: "inbound"})
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
func (s *stubTransport) Typing(ctx context.Context, chatID string) error { return nil }

// harness wires serve() to in-memory pipes and provides frame-level
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
		h.done <- serve(cfg, sdkIn, sdkOut, io.Discard)
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

	h.send(connproto.HelloAckFromHost{Type: "hello_ack", Protocol: 1, DataDir: t.TempDir()})

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
	err := serve(Config{Name: "x"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "NewTransport") {
		t.Errorf("err = %v", err)
	}
}
