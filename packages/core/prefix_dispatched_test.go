package core

import (
	"context"
	"encoding/json"
	"testing"
)

// DispatchedPrefix hands a side computation the exact system prompt and tools
// array the conversation last put on the wire, so the side request READS that
// cached prefix instead of paying a full-price read of the whole transcript.
//
// The assertion compares the reader against what the spy client actually
// received, rather than against what the test set up. A reader that agreed with
// the test's intention but not with the wire would align with nothing.
func TestDispatchedPrefixReturnsWhatWentOnTheWire(t *testing.T) {
	client := &prefixSpyClient{name: "spy"}
	a := NewAgent(client, "spy-model", "the system prompt", Registry{"noop": noopTool{}})

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}

	sent := client.calls()
	if len(sent) != 1 {
		t.Fatalf("spy client saw %d requests; want 1", len(sent))
	}
	if len(sent[0].Tools) == 0 {
		t.Fatal("the turn advertised no tools, so this test cannot tell an aligned prefix from an empty one")
	}

	system, tools, ok := a.DispatchedPrefix(client, "spy-model")
	if !ok {
		t.Fatal("not ok after a dispatch that landed; there is a warm prefix and the caller needs it")
	}
	if system != sent[0].System {
		t.Errorf("system = %q; the wire got %q", system, sent[0].System)
	}
	want, err := json.Marshal(sent[0].Tools)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("tools differ from the wire, so a request built on them misses the cache:\n got:  %s\n wire: %s", got, want)
	}
}

// Before any turn there is no warm prefix to aim at, so the caller must be told
// to keep whatever it did before rather than handed empty strings it cannot
// tell apart from a real prefix.
func TestDispatchedPrefixIsNotOkBeforeTheFirstDispatch(t *testing.T) {
	client := &prefixSpyClient{name: "spy"}
	a := NewAgent(client, "spy-model", "the system prompt", Registry{"noop": noopTool{}})

	if _, _, ok := a.DispatchedPrefix(client, "spy-model"); ok {
		t.Fatal("ok before any dispatch; nothing is cached, so there is no prefix to align to")
	}
}

// A prefix is warm only for the endpoint and model that wrote it. After a
// /model switch the retained prefix still describes the OUTGOING pair, and
// handing it to the incoming one would return bytes that align with nothing.
func TestDispatchedPrefixRefusesAnotherModelOrClient(t *testing.T) {
	outgoing := &prefixSpyClient{name: "outgoing"}
	incoming := &prefixSpyClient{name: "incoming"}
	a := NewAgent(outgoing, "outgoing-model", "the system prompt", Registry{"noop": noopTool{}})

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	a.SetClientAndModel(incoming, "incoming-model")

	if _, _, ok := a.DispatchedPrefix(incoming, "incoming-model"); ok {
		t.Error("ok for the incoming pair; that model has never seen this conversation")
	}
	if _, _, ok := a.DispatchedPrefix(outgoing, "different-model"); ok {
		t.Error("ok for a different model on the same client; the cache is keyed on the model too")
	}
	if _, _, ok := a.DispatchedPrefix(incoming, "outgoing-model"); ok {
		t.Error("ok for the same model on a different endpoint; the other endpoint holds no cache for it")
	}
	if _, _, ok := a.DispatchedPrefix(outgoing, "outgoing-model"); !ok {
		t.Error("not ok for the pair that actually dispatched; that prefix is still the warm one")
	}
}

// The reader must not hand out a window into the retained record. That record
// is the only surviving description of what the provider cached, and a caller
// that mutated it would corrupt the compaction target as a side effect.
func TestDispatchedPrefixCopiesTheToolsSlice(t *testing.T) {
	client := &prefixSpyClient{name: "spy"}
	a := NewAgent(client, "spy-model", "the system prompt", Registry{"noop": noopTool{}})

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}

	first, tools, ok := a.DispatchedPrefix(client, "spy-model")
	if !ok || len(tools) == 0 {
		t.Fatalf("no prefix to test against (ok=%v, %d tools)", ok, len(tools))
	}
	_ = first
	tools[0].Name = "clobbered"

	_, again, _ := a.DispatchedPrefix(client, "spy-model")
	if again[0].Name == "clobbered" {
		t.Fatal("a caller's write reached the retained prefix; the record must outlive the call unchanged")
	}
}
