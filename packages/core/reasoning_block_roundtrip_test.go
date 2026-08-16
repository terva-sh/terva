package core

import (
	"encoding/json"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
)

// A ReasoningBlock is replay state: the provider issued it and wants it handed
// back. Every field on it therefore has to survive BOTH round-trips terva puts
// a transcript through — the session file (resume) and the control-plane wire
// (a client that renders a transcript and returns it) — or a turn that was
// replayable in memory stops being replayable the moment it is written down.
//
// Written self-enrolling: the field list is scanned off the struct, not typed
// out here, so a field added later is covered by this guard on its first run
// rather than whenever someone remembers. That is the whole point — Shape was
// added because Summary+Encrypted could not say which provider a block came
// from, and a field that silently fails to persist reintroduces exactly that.

// distinctReasoningBlock fills every field of a ReasoningBlock with a value
// unique to that field, so a round-trip that crosses two fields over is caught
// as surely as one that drops them.
func distinctReasoningBlock(t *testing.T) provider.ReasoningBlock {
	t.Helper()
	var rb provider.ReasoningBlock
	v := reflect.ValueOf(&rb).Elem()
	ty := v.Type()
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if !v.Field(i).CanSet() {
			t.Fatalf("ReasoningBlock.%s is unexported; this guard cannot populate it "+
				"and cannot vouch for it either", f.Name)
		}
		switch f.Type.Kind() {
		case reflect.String:
			v.Field(i).SetString("value-of-" + f.Name)
		default:
			t.Fatalf("ReasoningBlock.%s is a %s, which this guard does not know how to "+
				"populate. Teach it, or the field rides both round-trips unchecked.",
				f.Name, f.Type.Kind())
		}
	}
	return rb
}

func TestReasoningBlockSurvivesTheSessionFile(t *testing.T) {
	want := distinctReasoningBlock(t)

	// Through the same encode/decode a resumed session uses, including the
	// JSON hop: a field the struct carries but the tags drop would otherwise
	// pass on an in-process comparison.
	encoded := encodeWireBlocks([]provider.Content{want})
	if len(encoded) != 1 {
		t.Fatalf("encoded %d blocks, want 1", len(encoded))
	}
	raw, err := json.Marshal(encoded[0])
	if err != nil {
		t.Fatal(err)
	}
	var back wireBlock
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got, ok := decodeWireBlock(back)
	if !ok {
		t.Fatalf("decodeWireBlock refused the block it just encoded: %s", raw)
	}
	if !reflect.DeepEqual(got, provider.Content(want)) {
		t.Errorf("session round-trip lost or crossed fields:\n got %#v\nwant %#v\n json %s", got, want, raw)
	}
}

func TestReasoningBlockSurvivesTheControlPlaneWire(t *testing.T) {
	want := distinctReasoningBlock(t)

	wire := ContentToWire([]provider.Content{want})
	if len(wire) != 1 {
		t.Fatalf("encoded %d blocks, want 1", len(wire))
	}
	raw, err := json.Marshal(wire[0])
	if err != nil {
		t.Fatal(err)
	}
	var back WireBlock
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := ContentFromWire([]WireBlock{back})
	if len(got) != 1 {
		t.Fatalf("decoded %d blocks, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], provider.Content(want)) {
		t.Errorf("wire round-trip lost or crossed fields:\n got %#v\nwant %#v\n json %s", got[0], want, raw)
	}
}

// The legacy object-form hydration path reads a session file written before
// blocks carried a type discriminator, and discriminates by field presence
// instead. It is a third decoder for the same block, and it has its own copy of
// the field list — which is exactly the shape that goes stale.
func TestReasoningBlockSurvivesLegacyHydration(t *testing.T) {
	want := distinctReasoningBlock(t)

	raw, err := json.Marshal(encodeWireBlocks([]provider.Content{want})[0])
	if err != nil {
		t.Fatal(err)
	}
	// The v1 form is the same object without its "type" tag.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	delete(obj, "type")
	blockJSON, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	msgJSON, err := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": []json.RawMessage{blockJSON},
	})
	if err != nil {
		t.Fatal(err)
	}

	var rep loadReport
	msg, err := hydrateMessageObject(msgJSON, &rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("hydrated %d blocks, want 1: %#v", len(msg.Content), msg.Content)
	}
	if !reflect.DeepEqual(msg.Content[0], provider.Content(want)) {
		t.Errorf("legacy hydration lost or crossed fields:\n got %#v\nwant %#v\n json %s",
			msg.Content[0], want, blockJSON)
	}
}
