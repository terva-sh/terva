package core

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/provider"
)

// compactionSummary is the message Compact leaves in place of the turns it
// folded away — built here the way compact.go builds it, so this test fails if
// the marker keys ever drift apart from the wire's view of them.
func compactionSummary() provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)\n\nwe fixed the thing"}},
		Meta: map[string]string{
			MetaCompaction:   "true",
			MetaTokensBefore: "112000",
		},
	}
}

// TestCompactionSurvivesWireRoundTrip pins the bug this file exists for:
// WireMessage carried no compaction marker, so a compaction summary crossing to
// any client arrived as an ordinary RoleUser message. Every display surface then
// drew it as a user bubble containing raw "## Context Summary" markdown, and the
// TUI's collapsed/expandable compaction block (which keys off Meta) was dead
// code — in a TUI that is now always carrier-backed.
//
// The renderers key off Meta, so the round trip must restore Meta — not merely
// set the wire fields.
func TestCompactionSurvivesWireRoundTrip(t *testing.T) {
	w := MessageToWireFull(compactionSummary())
	if !w.Compaction {
		t.Fatal("wire message lost the compaction marker")
	}
	if w.TokensBefore != 112000 {
		t.Fatalf("tokens_before = %d, want 112000", w.TokensBefore)
	}

	// Through real JSON, not just the struct: an in-process carrier hands the
	// struct over directly, but a WebSocket client only ever sees the bytes.
	var decoded WireMessage
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := MessageFromWire(decoded)
	if got.Meta[MetaCompaction] != "true" {
		t.Errorf("Meta[%q] = %q after round trip, want \"true\" — renderers key off this",
			MetaCompaction, got.Meta[MetaCompaction])
	}
	if got.Meta[MetaTokensBefore] != "112000" {
		t.Errorf("Meta[%q] = %q after round trip, want \"112000\"",
			MetaTokensBefore, got.Meta[MetaTokensBefore])
	}
}

// TestOrdinaryMessageCarriesNoCompactionMarker is the negative control: the
// marker must not leak onto messages that are not compaction summaries, or every
// user turn renders as a divider.
func TestOrdinaryMessageCarriesNoCompactionMarker(t *testing.T) {
	plain := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	}
	w := MessageToWireFull(plain)
	if w.Compaction || w.TokensBefore != 0 {
		t.Fatalf("plain message picked up a compaction marker: %+v", w)
	}
	if got := MessageFromWire(w); got.Meta[MetaCompaction] != "" {
		t.Errorf("plain message gained Meta[%q] = %q", MetaCompaction, got.Meta[MetaCompaction])
	}
}

// TestSyntheticAndCompactionCoexist guards the Meta map construction in
// MessageFromWire: it used to assign a fresh single-key map, so adding a second
// marker naively would have clobbered the first.
func TestSyntheticAndCompactionCoexist(t *testing.T) {
	m := compactionSummary()
	m.Meta[MetaSynthetic] = "true"

	got := MessageFromWire(MessageToWireFull(m))
	if got.Meta[MetaSynthetic] != "true" || got.Meta[MetaCompaction] != "true" {
		t.Fatalf("markers did not coexist: %#v", got.Meta)
	}
}

// TestCompactionTokensBeforeTolerablyMalformed: a bad count must not cost the
// divider its marker. The label degrades; the boundary still renders.
func TestCompactionTokensBeforeTolerablyMalformed(t *testing.T) {
	m := compactionSummary()
	m.Meta[MetaTokensBefore] = "not-a-number"

	w := MessageToWireFull(m)
	if !w.Compaction {
		t.Fatal("a malformed token count dropped the compaction marker")
	}
	if w.TokensBefore != 0 {
		t.Fatalf("TokensBefore = %d, want 0 for a malformed count", w.TokensBefore)
	}
	if got := MessageFromWire(w); got.Meta[MetaCompaction] != "true" {
		t.Error("marker lost on the way back")
	}
}
