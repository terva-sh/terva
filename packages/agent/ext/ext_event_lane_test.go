package ext

import (
	"encoding/json"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// OnCompactStart's godoc told authors to call read_session from the handler:
// "the handler has time to read the full session (read_session) and harvest
// detail before it's summarized away". docs/extensions.md said the same.
//
// It deadlocked. Event handlers ran INLINE on the Run read loop, which is the
// only goroutine that can deliver the reply to a request the handler makes. A
// cooperative host answered promptly, the reply sat unread in the pipe, and the
// extension was wire-dead — no tool_call, no event, no panel_key — until the 30s
// request timeout, at which point ReadSession returned an error and the memory
// or index extension the feature exists for harvested nothing.
//
// This is the deterministic form of that: fire the event, answer the request the
// handler makes, and require the handler to finish. A test that merely called
// the handler directly would prove nothing — the defect was in WHERE it ran.
func TestARequestIssuedFromAnEventHandlerCompletes(t *testing.T) {
	h := newHarness("test-ext")

	got := make(chan []SessionMessage, 1)
	failed := make(chan error, 1)
	h.ext.OnCompactStart(func(CompactStart) {
		msgs, err := h.ext.ReadSession("s1")
		if err != nil {
			failed <- err
			return
		}
		got <- msgs
	})

	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "compact_start", Text: "context is full"})

	// The handler's request must reach the host. Under the old inline dispatch
	// it did — the read loop had already written the frame before blocking —
	// so the frame arriving proves nothing on its own; the reply below is the
	// half that used to hang.
	f := h.drainUntil(t, "read_session")
	var rs extproto.ReadSessionFromExt
	if err := json.Unmarshal(f.raw, &rs); err != nil {
		t.Fatal(err)
	}
	h.sendToExt(t, extproto.SessionDataFromHost{
		Type: "session_data", ID: rs.ID,
		Messages: []extproto.SessionMessage{{Role: "user", Text: "harvest me"}},
	})

	select {
	case msgs := <-got:
		if len(msgs) != 1 || msgs[0].Text != "harvest me" {
			t.Errorf("ReadSession from the handler returned %+v", msgs)
		}
	case err := <-failed:
		t.Fatalf("ReadSession from an event handler failed: %v — the read loop cannot deliver the "+
			"reply while it is inside the handler waiting for it", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished: its request was answered and the reply was never routed, " +
			"which is the read-loop reentrancy the event lane exists to remove")
	}
}

// The wire must stay live WHILE a handler is blocked. This is the half that
// makes the failure operational rather than academic: under inline dispatch the
// whole extension went dark for the duration, so a tool call the model issued
// during a compaction simply never ran.
func TestTheWireStaysLiveWhileAnEventHandlerIsBlocked(t *testing.T) {
	h := newHarness("test-ext")

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	h.ext.Tool("ping", "", json.RawMessage(`{"type":"object"}`), func(json.RawMessage) ToolResult {
		return ToolResult{Content: []ToolContent{Text("pong")}}
	})
	h.ext.OnCompactStart(func(CompactStart) {
		entered <- struct{}{}
		<-release // park, as a real handler does while waiting on a reply
	})

	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "compact_start"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}

	// With the handler parked, a tool call must still be served.
	h.sendToExt(t, extproto.ToolCallFromHost{Type: "tool_call", ID: "c1", Name: "ping", Args: json.RawMessage(`{}`)})
	f := h.drainUntil(t, "tool_result")
	close(release)

	var tr extproto.ToolResultFromExt
	if err := json.Unmarshal(f.raw, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Content) != 1 || tr.Content[0].Text != "pong" {
		t.Errorf("tool_result = %+v, want one text block \"pong\"", tr.Content)
	}
}

// Handlers must stay in ARRIVAL order. A goroutine per event would have fixed
// the deadlock and broken this, which OnSession/OnSessionEnd depend on and which
// inline dispatch guaranteed for free.
func TestEventHandlersRunInArrivalOrder(t *testing.T) {
	h := newHarness("test-ext")

	const n = 25
	seen := make(chan string, n*2)
	h.ext.OnUserMessage(func(m UserMessage) { seen <- "user:" + m.Text })
	h.ext.OnCompactStart(func(CompactStart) { seen <- "compact" })

	go h.ext.Run()
	h.handshake(t)

	var want []string
	for i := 0; i < n; i++ {
		text := string(rune('a' + i%26))
		h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "user_message", Text: text})
		want = append(want, "user:"+text)
		h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "compact_start"})
		want = append(want, "compact")
	}

	for i, w := range want {
		select {
		case got := <-seen:
			if got != w {
				t.Fatalf("event %d ran out of order: got %q, want %q", i, got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d events ran", i, len(want))
		}
	}
}
