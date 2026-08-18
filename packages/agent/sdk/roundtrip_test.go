package sdk

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The round-trip this package's own doc points embedders at: NoSess is forced,
// "use Messages/SetMessages" to persist and restore a conversation.
//
// It was lossy in three separate ways, and none of them errored:
//
//   - Messages() called core.MessageToWire, the LEAN form. That form drops image
//     Data and keeps only Bytes, because a carrier's recipient can fetch the
//     payload another way. An embedder cannot, so an image came back with no
//     pixels and the next request replayed it empty.
//   - SetMessages built provider.Message{Role, Content} by hand, dropping Time
//     and the whole Meta map. MetaSynthetic and MetaCompaction went with it: a
//     harness nudge became indistinguishable from real user input, and the
//     compaction checkpoint marker title_seed.go keys on disappeared.
//   - rebuildContent was a private twin of core.ContentFromWire missing the
//     compaction_summary arm, so the blob a compaction replaced — which terva
//     cannot rebuild — was dropped outright.
//
// TestRebuildContentRoundTrip looked like it covered this. It asserted len()
// plus two blocks, drove the private converters rather than the public methods,
// and its comment ("Image bytes are carried ... so they survive too") was false
// for core.ContentToWire, which is the lean form.
//
// This drives the REAL Messages()/SetMessages() pair, because the defect was in
// which converter each one reached for — not in the converters.
func TestTheDocumentedTranscriptRoundTripLosesNothing(t *testing.T) {
	sent := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	orig := []provider.Message{
		{
			Role: provider.RoleUser,
			Time: sent,
			Meta: map[string]string{core.MetaSynthetic: "true"},
			Content: []provider.Content{
				provider.TextBlock{Text: "look at this"},
				provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3, 4}},
			},
		},
		{
			Role: provider.RoleUser,
			Meta: map[string]string{core.MetaCompaction: "true", core.MetaTokensBefore: "112000"},
			Content: []provider.Content{
				provider.TextBlock{Text: "## Context Summary (compacted)"},
			},
		},
		{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.ToolCallBlock{ID: "t1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
				provider.ReasoningBlock{ID: "rs_1", Summary: "weighing options", Encrypted: "OPAQUE"},
			},
		},
	}

	r := &Runtime{agent: core.NewAgent(nil, "m", "", core.Registry{})}
	r.agent.SetMessages(orig)

	// persist … restore, exactly as the package doc describes.
	wire := r.Messages()
	r.SetMessages(wire)
	got := r.agent.Messages()

	if len(got) != len(orig) {
		t.Fatalf("message count changed: got %d, want %d", len(got), len(orig))
	}

	// 1. The image payload.
	img, ok := got[0].Content[1].(provider.ImageBlock)
	if !ok {
		t.Fatalf("block is %T, want provider.ImageBlock", got[0].Content[1])
	}
	if !bytes.Equal(img.Data, []byte{1, 2, 3, 4}) {
		t.Errorf("image payload lost on round-trip: got %v, want [1 2 3 4] — the next request replays an image with no pixels", img.Data)
	}

	// 2. Meta, on both messages that carry it.
	if got[0].Meta[core.MetaSynthetic] != "true" {
		t.Errorf("MetaSynthetic lost: %v — a harness nudge is now indistinguishable from real user input", got[0].Meta)
	}
	if got[1].Meta[core.MetaCompaction] != "true" {
		t.Errorf("MetaCompaction lost: %v — the checkpoint marker title_seed.go keys on is gone", got[1].Meta)
	}
	if got[1].Meta[core.MetaTokensBefore] != "112000" {
		t.Errorf("MetaTokensBefore lost: %v", got[1].Meta)
	}

	// 3. Time.
	if !got[0].Time.Equal(sent) {
		t.Errorf("Time lost: got %v, want %v", got[0].Time, sent)
	}

	// 4. The blocks that already worked, kept so the fix cannot read as
	//    narrowing coverage.
	if tc, ok := got[2].Content[0].(provider.ToolCallBlock); !ok || tc.ID != "t1" || tc.Name != "read" {
		t.Errorf("tool_call did not round-trip: %+v", got[2].Content[0])
	}
	rb, ok := got[2].Content[1].(provider.ReasoningBlock)
	if !ok || rb.ID != "rs_1" || rb.Encrypted != "OPAQUE" {
		t.Errorf("reasoning block did not round-trip: %+v", got[2].Content[1])
	}
}

// A compaction SUMMARY block is the one content kind the private twin had no arm
// for, so it vanished rather than degrading. Asserted separately because it
// travels as a block, not as message Meta, and the two were lost by different
// mechanisms.
func TestACompactionSummaryBlockSurvivesTheRoundTrip(t *testing.T) {
	orig := []provider.Message{{
		Role: provider.RoleUser,
		Content: []provider.Content{
			provider.TextBlock{Text: "## Context Summary (compacted)"},
			provider.CompactionBlock{ID: "cmp_1", Encrypted: "OPAQUE-BLOB", Provider: "openai-codex"},
		},
	}}

	r := &Runtime{agent: core.NewAgent(nil, "m", "", core.Registry{})}
	r.agent.SetMessages(orig)
	r.SetMessages(r.Messages())
	got := r.agent.Messages()

	if len(got) != 1 || len(got[0].Content) != 2 {
		t.Fatalf("content count changed: %+v — the compaction block is the one terva cannot rebuild", got)
	}
	cb, ok := got[0].Content[1].(provider.CompactionBlock)
	if !ok {
		t.Fatalf("block is %T, want provider.CompactionBlock", got[0].Content[1])
	}
	if cb.ID != "cmp_1" || cb.Encrypted != "OPAQUE-BLOB" {
		t.Errorf("compaction block lost the blob terva cannot rebuild: %+v", cb)
	}
	// Provenance specifically: a blob that arrives without it is replayable by
	// nobody, so losing this field silently turns a live compaction foreign to
	// its own issuer.
	if cb.Provider != "openai-codex" {
		t.Errorf("compaction provenance lost: %+v", cb)
	}
}
