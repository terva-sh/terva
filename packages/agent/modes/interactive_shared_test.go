package modes

// The TUI's end of share_file: the tool_result fold has to record the files a
// call published onto the tool-role message it folds into, because that message
// — not the live tool-call overlay — is what the renderer reads. The overlay is
// suppressed the moment a result reaches the transcript, so a card hung off it
// would flash and vanish.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// sharesOn reads back the shares recorded on a message, the way the renderer
// does.
func sharesOn(t *testing.T, m provider.Message) []core.SharedFile {
	t.Helper()
	raw := m.Meta[core.MetaShared]
	if raw == "" {
		return nil
	}
	var out []core.SharedFile
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("Meta[%s] = %q: %v", core.MetaShared, raw, err)
	}
	return out
}

// toolResultWithShares is the wire event the daemon sends when a call published
// a file.
func toolResultWithShares(id string, shared ...core.SharedFile) ctrlproto.Event {
	return ctrlproto.Event{WireEvent: core.WireEvent{
		Type:   "tool_result",
		ID:     id,
		Result: []core.WireBlock{{Type: "text", Text: "shared"}},
		Shared: shared,
	}}
}

// Two constants hold this key in packages that cannot import each other
// (packages/tui must not depend on packages/core). This package imports both,
// so it is where the duplication stops being a silent break waiting for whoever
// renames one of them.
func TestTUISharedMetaKeyMatchesCore(t *testing.T) {
	if tui.MetaSharedKey != core.MetaShared {
		t.Errorf("tui.MetaSharedKey = %q, core.MetaShared = %q — the renderer is reading a key nothing writes",
			tui.MetaSharedKey, core.MetaShared)
	}
}

// The live path: a share on a tool_result lands on the transcript message, in
// the key the renderer reads.
func TestCarrierToolResultRecordsShares(t *testing.T) {
	i := newCtrlprotoTestInteractive()

	i.appendCarrierToolResultLocked(toolResultWithShares("call_1", core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "report.pdf", Kind: "document", Size: 2048,
	}))

	msgs := i.carrierTranscript()
	if len(msgs) != 1 {
		t.Fatalf("transcript has %d messages, want the tool-role one", len(msgs))
	}
	got := sharesOn(t, msgs[0])
	if len(got) != 1 || got[0].ID != "shr_a" || got[0].Name != "report.pdf" {
		t.Fatalf("recorded shares = %+v, want the published one", got)
	}
}

// One tool-role message batches a whole step's results, so the fold has to
// MERGE. Assigning would erase the first share of every step that shared twice
// — and the renderer would then draw the second card under both boxes.
func TestCarrierToolResultMergesSharesAcrossAStep(t *testing.T) {
	i := newCtrlprotoTestInteractive()

	i.appendCarrierToolResultLocked(toolResultWithShares("call_1", core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "alpha.md",
	}))
	i.appendCarrierToolResultLocked(toolResultWithShares("call_2", core.SharedFile{
		ID: "shr_b", CallID: "call_2", Name: "beta.md",
	}))

	msgs := i.carrierTranscript()
	if len(msgs) != 1 {
		t.Fatalf("consecutive results should fold into one message, got %d", len(msgs))
	}
	got := sharesOn(t, msgs[0])
	if len(got) != 2 {
		t.Fatalf("recorded %d shares, want both: %+v", len(got), got)
	}
	if got[0].ID != "shr_a" || got[1].ID != "shr_b" {
		t.Errorf("shares = %+v, want alpha then beta with their own call ids", got)
	}
	if got[0].CallID != "call_1" || got[1].CallID != "call_2" {
		t.Errorf("call ids = %q, %q; want call_1, call_2 so each card finds its box",
			got[0].CallID, got[1].CallID)
	}
}

// A result that shared nothing must not grow a Meta bag: every key in there is
// something the renderer branches on, and an empty one is a claim about the
// turn that is not true.
func TestCarrierToolResultWithoutSharesLeavesMetaAlone(t *testing.T) {
	i := newCtrlprotoTestInteractive()

	i.appendCarrierToolResultLocked(ctrlproto.Event{WireEvent: core.WireEvent{
		Type:   "tool_result",
		ID:     "call_1",
		Result: []core.WireBlock{{Type: "text", Text: "ok"}},
	}})

	msgs := i.carrierTranscript()
	if len(msgs) != 1 {
		t.Fatalf("transcript has %d messages, want one", len(msgs))
	}
	if msgs[0].Meta != nil {
		t.Errorf("Meta = %v on a result that shared nothing, want nil", msgs[0].Meta)
	}
}

// The renderer has to be able to read what the fold wrote. This is the join the
// two halves meet at: the fold writes core.SharedFile through core's key, and
// the TUI parses tui.SharedFile out of the same bag.
func TestRecordedSharesRenderAsACard(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.appendCarrierToolResultLocked(toolResultWithShares("call_1", core.SharedFile{
		ID: "shr_a", CallID: "call_1", Name: "deliverable.pdf", Kind: "document", Size: 2048,
	}))

	v := tui.View{Theme: tui.Dark, Messages: i.carrierTranscript()}
	if plain := stripANSI(v.Build(80)); !strings.Contains(plain, "deliverable.pdf") {
		t.Errorf("the fold's record did not reach a card:\n%s", plain)
	}
}
