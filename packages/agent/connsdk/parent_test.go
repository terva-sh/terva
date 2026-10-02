package connsdk

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/connproto"
)

type parentTransport struct{ stubTransport }

func (s *parentTransport) Receive(ctx context.Context, deliver func(Message)) error {
	deliver(Message{ID: "m", ChatID: "thread", ChatKind: "thread", ParentChatID: "room", ParentChatKind: "dm", UserID: "owner", Text: "hi"})
	<-ctx.Done()
	return ctx.Err()
}

func TestServeNegotiatesParentContext(t *testing.T) {
	for _, tc := range []struct {
		protocol        int
		connector, host bool
	}{
		{2, true, true}, {2, true, false}, {2, false, true}, {1, true, true},
	} {
		tr := &parentTransport{}
		cfg := testConfig(tr)
		if tc.connector {
			cfg.Capabilities.Features = []string{"chat_parents"}
		}
		h := newHarness(t, cfg)
		h.next("hello")
		ack := connproto.HelloAckFromHost{Type: "hello_ack", Protocol: tc.protocol}
		if tc.host {
			ack.Capabilities = &connproto.Capabilities{Features: []string{"chat_parents"}}
		}
		h.send(ack)
		h.send(connproto.ConnectFromHost{Type: "connect"})
		h.next("connected")
		m := h.next("message")
		if tc.protocol == 2 && tc.connector && tc.host {
			if m["parent_chat_id"] != "room" || m["parent_chat_kind"] != "dm" || m["chat_id"] != "thread" {
				t.Fatalf("frame = %v", m)
			}
		} else if m["parent_chat_id"] != nil || m["parent_chat_kind"] != nil {
			t.Fatalf("metadata leaked: %v", m)
		}
		h.send(connproto.ShutdownFromHost{Type: "shutdown"})
		if err := h.serveErr(); err != nil {
			t.Fatal(err)
		}
	}
}
