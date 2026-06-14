package sdk

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// TestRebuildContentRoundTrip pins that every content block kind the
// canonical wire carries survives provider -> wire -> provider through
// the SDK's rebuildContent. Reasoning blocks are the regression this
// guards: they used to drop on the wire, so SetMessages silently lost
// encrypted chain-of-thought that providers like OpenAI Codex require
// replayed on follow-up turns.
func TestRebuildContentRoundTrip(t *testing.T) {
	orig := []provider.Content{
		provider.TextBlock{Text: "hello"},
		provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
		provider.ToolCallBlock{ID: "t1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
		provider.ToolResultBlock{CallID: "t1", IsError: true, Content: []provider.Content{provider.TextBlock{Text: "boom"}}},
		provider.ReasoningBlock{ID: "rs_1", Summary: "weighing options", Encrypted: "OPAQUE"},
	}

	// provider -> wire (the outbound path get_messages/SetMessages share)
	// then wire -> provider (rebuildContent). Image bytes are carried for
	// inbound payloads, so they survive too.
	wire := core.ContentToWire(orig)
	got := rebuildContent(wire)

	if len(got) != len(orig) {
		t.Fatalf("block count changed: got %d, want %d", len(got), len(orig))
	}

	rb, ok := got[4].(provider.ReasoningBlock)
	if !ok {
		t.Fatalf("block 4 is %T, want provider.ReasoningBlock", got[4])
	}
	if rb.ID != "rs_1" || rb.Summary != "weighing options" || rb.Encrypted != "OPAQUE" {
		t.Fatalf("reasoning block lost data on round-trip: %+v", rb)
	}

	if tr, ok := got[3].(provider.ToolResultBlock); !ok || tr.CallID != "t1" || !tr.IsError {
		t.Fatalf("tool_result did not round-trip: %+v", got[3])
	}
}
