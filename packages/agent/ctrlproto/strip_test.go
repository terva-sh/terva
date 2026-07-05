package ctrlproto

import (
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// TestStripImageData: stripping is copy-on-write — the original event (one
// value shared by every subscriber) keeps its payloads — and the stripped
// copy matches the lean wire shape exactly (Bytes stays, Data goes),
// including snapshot messages and nested tool-result content.
func TestStripImageData(t *testing.T) {
	img := core.WireBlock{Type: "image", MimeType: "image/png", Data: []byte{1, 2}, Bytes: 2}

	ev := Event{WireEvent: core.WireEvent{Type: "user_message", Message: &core.WireMessage{
		Role: "user", Content: []core.WireBlock{img, {Type: "text", Text: "hi"}},
	}}}
	got := stripImageData(ev)
	if b := got.Message.Content[0]; b.Data != nil || b.Bytes != 2 || b.MimeType != "image/png" {
		t.Fatalf("stripped block = %+v, want lean shape", b)
	}
	if got.Message.Content[1].Text != "hi" {
		t.Fatal("sibling block lost in the copy")
	}
	if b := ev.Message.Content[0]; len(b.Data) != 2 {
		t.Fatal("original event was mutated")
	}

	tr := Event{WireEvent: core.WireEvent{Type: "tool_result", Result: []core.WireBlock{
		{Type: "tool_result", CallID: "t1", Content: []core.WireBlock{img}},
	}}}
	if b := stripImageData(tr).Result[0].Content[0]; b.Data != nil || b.Bytes != 2 {
		t.Fatalf("nested strip failed: %+v", b)
	}
	if len(tr.Result[0].Content[0].Data) != 2 {
		t.Fatal("nested original mutated")
	}

	snap := Event{WireEvent: core.WireEvent{Type: EventSnapshot}, Snapshot: &Snapshot{
		Messages: []core.WireMessage{{Role: "user", Content: []core.WireBlock{img}}},
	}}
	if b := stripImageData(snap).Snapshot.Messages[0].Content[0]; b.Data != nil {
		t.Fatalf("snapshot strip failed: %+v", b)
	}
	if len(snap.Snapshot.Messages[0].Content[0].Data) != 2 {
		t.Fatal("snapshot original mutated")
	}

	// A payload-free event passes through without copying.
	plain := Event{WireEvent: core.WireEvent{Type: "text_delta", Delta: "x"}}
	if got := stripImageData(plain); got.Delta != "x" {
		t.Fatal("plain event should pass through unchanged")
	}
}

// TestServeConnImageDataNegotiation: the serve loop is the serialization
// boundary for image payloads — from the same broadcast, a client that
// negotiated "image-data" receives the full form while one that didn't
// receives the lean shape (Data stripped, size metadata kept).
func TestServeConnImageDataNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		wantData bool
	}{
		{"negotiated", []string{FeatureImageData}, true},
		{"lean", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSvc()
			client, server := newMemPair()
			go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: []Group{GroupConversation}, Features: tc.features}))
			if sh := pull(t, client); sh.Kind != KindHello {
				t.Fatalf("expected server hello, got %+v", sh)
			}
			push(t, client, mustCmd(t, 1, "s1", MethodSubscribe, nil))
			if r := pull(t, client); r.Kind != KindResp || r.Error != nil {
				t.Fatalf("subscribe resp: %+v", r)
			}

			svc.broadcast("s1", ConversationEvent(core.WireEvent{
				Type: "user_message",
				Message: &core.WireMessage{Role: "user", Content: []core.WireBlock{
					{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, Bytes: 3},
				}},
			}))

			deadline := time.After(3 * time.Second)
			for {
				select {
				case <-deadline:
					t.Fatal("user_message event never arrived")
				default:
				}
				f := pull(t, client)
				if f.Kind != KindEvent || f.Event == nil || f.Event.Type != "user_message" {
					continue
				}
				b := f.Event.Message.Content[0]
				if got := len(b.Data) > 0; got != tc.wantData {
					t.Fatalf("data present = %v, want %v (block %+v)", got, tc.wantData, b)
				}
				if b.Bytes != 3 || b.MimeType != "image/png" {
					t.Fatalf("size metadata must survive either way: %+v", b)
				}
				return
			}
		})
	}
}
