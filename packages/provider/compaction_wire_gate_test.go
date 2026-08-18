package provider

import (
	"strings"
	"testing"
)

// foreignBlob is a compaction summary issued by somebody else.
const foreignBlobBytes = "ENCRYPTED-BLOB-FROM-ANOTHER-PROVIDER"

func compactionRequest(model, issuer string) Request {
	return Request{Model: model, Messages: []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "carry on"}}},
		{Role: RoleAssistant, Content: []Content{
			CompactionBlock{ID: "cmp_1", Encrypted: foreignBlobBytes, Provider: issuer},
		}},
	}}
}

// No wire may carry a compaction blob it did not issue.
//
// A CompactionBlock is opaque and provider-issued. Four of the five builders
// have no arm for it at all — their content switches have no default, so it
// vanished silently, which for a compaction is amnesia rather than degradation:
// the blob is the ONLY encoding of the assistant turns it replaced. The fifth,
// codex, had the opposite bug — it replayed ANY compaction block as its own
// `compaction_summary`, so another vendor's bytes would have gone out under an
// OpenAI item type.
//
// Runs over the same AST-enrolled table as the image-input gate, so a sixth
// provider is covered by having been added rather than by being remembered.
func TestNoWireBuilderCarriesAForeignCompactionBlob(t *testing.T) {
	for _, b := range wireBuilders {
		t.Run(b.receiver, func(t *testing.T) {
			installGateModel(t, b, true)
			wire := marshalWire(t, b.build(t, compactionRequest(b.model, "some-other-provider")))
			if strings.Contains(wire, foreignBlobBytes) {
				t.Errorf("%s put another provider's compaction blob on the wire", b.receiver)
			}
		})
	}
}

// The complement, and the thing that keeps the test above from passing because
// codex stopped replaying compactions altogether: its OWN blob must still go.
// Losing this is losing the feature — a codex session that compacted server-side
// and then could not replay its own summary would re-send nothing in its place.
func TestCodexStillReplaysItsOwnCompactionBlob(t *testing.T) {
	var codex *wireBuilder
	for i := range wireBuilders {
		if wireBuilders[i].receiver == "codexClient" {
			codex = &wireBuilders[i]
		}
	}
	if codex == nil {
		t.Fatal("no codexClient entry in wireBuilders; this test is not testing what it says")
	}
	installGateModel(t, *codex, true)

	wire := marshalWire(t, codex.build(t, compactionRequest(codex.model, "openai-codex")))
	if !strings.Contains(wire, foreignBlobBytes) {
		t.Error("codex dropped its own compaction summary — the turns it replaced have no other encoding")
	}
	if !strings.Contains(wire, "compaction_summary") {
		t.Error("codex sent the blob without the compaction_summary item type")
	}
}
