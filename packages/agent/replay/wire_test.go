package replay

import (
	"context"
	"io"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// memConn is an in-memory FrameConn routed through the real codec, so the
// end-to-end test exercises frame (de)serialization too.
type memConn struct {
	recv <-chan ctrlproto.Frame
	send chan<- ctrlproto.Frame
}

func (c *memConn) ReadFrame(ctx context.Context) (ctrlproto.Frame, error) {
	select {
	case <-ctx.Done():
		return ctrlproto.Frame{}, ctx.Err()
	case f, ok := <-c.recv:
		if !ok {
			return ctrlproto.Frame{}, io.EOF
		}
		return f, nil
	}
}

func (c *memConn) WriteFrame(ctx context.Context, f ctrlproto.Frame) error {
	b, err := ctrlproto.Encode(f)
	if err != nil {
		return err
	}
	d, err := ctrlproto.Decode(b)
	if err != nil {
		return err
	}
	select {
	case c.send <- d:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *memConn) Close() error { return nil }

// TestCarrierOverServeConn drives the whole stack headlessly: a recorded file →
// rows → synth → player → carrier → ServeConn → frames → a scripted client.
// Subscribing yields a snapshot; replay.control play streams the conversation
// events to a terminal "done".
func TestCarrierOverServeConn(t *testing.T) {
	// A fast pace keeps the test sub-second.
	fast := Pace{TextRunes: 100, TextInterval: time.Millisecond, Think: time.Millisecond, Tool: time.Millisecond, Compact: time.Millisecond}
	c, err := Open(writeFixture(t), Options{Pace: fast})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	a2b := make(chan ctrlproto.Frame, 128)
	b2a := make(chan ctrlproto.Frame, 128)
	client := &memConn{recv: b2a, send: a2b}
	server := &memConn{recv: a2b, send: b2a}
	hello := ctrlproto.ServerHello("terva-replay", "0")
	hello.Groups = append(hello.Groups, ctrlproto.GroupReplay)
	go ctrlproto.ServeConn(t.Context(), server, c, hello)

	write := func(f ctrlproto.Frame) {
		t.Helper()
		if err := client.WriteFrame(context.Background(), f); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	cmd := func(id uint64, m ctrlproto.Method, params any) ctrlproto.Frame {
		t.Helper()
		f, err := ctrlproto.CmdFrame(id, c.id, m, params)
		if err != nil {
			t.Fatalf("CmdFrame: %v", err)
		}
		return f
	}

	// Handshake negotiating conversation + replay.
	write(ctrlproto.HelloFrame(ctrlproto.Hello{
		Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol,
		Groups: []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupReplay},
	}))

	// Subscribe, then play. Collect frames until the terminal "done" event.
	write(cmd(1, ctrlproto.MethodSubscribe, nil))
	write(cmd(2, ctrlproto.MethodReplayControl, ctrlproto.ReplayControlParams{Action: "play"}))

	var gotSnapshot, gotUser, gotAssistant, gotReplayState, gotDone bool
	deadline := time.After(3 * time.Second)
	for !gotDone {
		select {
		case <-deadline:
			t.Fatalf("timeout; snapshot=%v user=%v assistant=%v done=%v", gotSnapshot, gotUser, gotAssistant, gotDone)
		case f := <-b2a:
			if f.Kind != ctrlproto.KindEvent || f.Event == nil {
				continue
			}
			switch f.Event.Type {
			case ctrlproto.EventSnapshot:
				gotSnapshot = true
			case ctrlproto.EventReplayState:
				gotReplayState = true
			case "user_message":
				gotUser = true
			case "assistant_message":
				gotAssistant = true
			case "done":
				gotDone = true
			}
		}
	}
	if !gotSnapshot || !gotUser || !gotAssistant || !gotReplayState {
		t.Errorf("missing events: snapshot=%v user=%v assistant=%v replay_state=%v", gotSnapshot, gotUser, gotAssistant, gotReplayState)
	}
}
