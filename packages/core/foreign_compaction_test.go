package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func compactedTranscript(issuer string) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.CompactionBlock{ID: "cmp_1", Encrypted: "opaque", Provider: issuer},
		}},
	}
}

// A blob the live provider cannot read becomes a note the model can see.
//
// provider.ForeignCompactions was written for exactly this and had no
// production caller, while its own doc said "asking once, here, is what makes
// it a decision". Nothing asked, so /model away from openai-codex after a
// server-side compaction handed the next model a conversation that reads
// continuous and is missing everything the blob stood for.
func TestAForeignCompactionBecomesAVisibleGap(t *testing.T) {
	msgs := compactedTranscript("openai-codex")

	out := replaceForeignCompactions(msgs, "anthropic")

	tb, ok := out[1].Content[0].(provider.TextBlock)
	if !ok {
		t.Fatalf("the blob is still a %T on a provider that cannot replay it — it will be dropped "+
			"by a content switch with no default arm, and the model will never know", out[1].Content[0])
	}
	if !strings.Contains(tb.Text, "not available on this model") {
		t.Errorf("the note does not tell the model the context is missing: %q", tb.Text)
	}
}

// The issuing provider still gets its own blob. Replacing it there would delete
// the only encoding of the turns it summarized.
func TestTheIssuingProviderKeepsItsCompaction(t *testing.T) {
	msgs := compactedTranscript("openai-codex")
	out := replaceForeignCompactions(msgs, "openai-codex")
	if _, ok := out[1].Content[0].(provider.CompactionBlock); !ok {
		t.Fatalf("openai-codex lost its own compaction blob (%T)", out[1].Content[0])
	}
}

// A blob predating provenance is left alone, on every provider.
//
// provider.ForeignCompactions reports it foreign to everyone, and this
// deliberately does not act on that. openai-codex is the only issuer in the
// tree, so an unattributed blob is one of its own from before
// CompactionBlock.Provider existed — replacing it with a note would delete the
// summarized history out of every such session, which is the harm this whole
// change exists to prevent. The first version of this did exactly that, and two
// existing round-trip tests failed and said so.
func TestAProvenancelessCompactionIsLeftAlone(t *testing.T) {
	for _, p := range []string{"openai-codex", "anthropic", ""} {
		out := replaceForeignCompactions(compactedTranscript(""), p)
		if _, ok := out[1].Content[0].(provider.CompactionBlock); !ok {
			t.Errorf("provider %q: an unattributed blob became %T. Every session compacted before "+
				"the Provider field existed carries one, and this is their only history.",
				p, out[1].Content[0])
		}
	}
}

// The request transcript is a snapshot sharing Content slices with the live
// session. The blob must survive there: switching BACK to the issuing provider
// has to replay it, and a session file that lost it could never be resumed on
// the model that made it.
func TestReplacingForeignCompactionsLeavesTheSessionIntact(t *testing.T) {
	msgs := compactedTranscript("openai-codex")

	_ = replaceForeignCompactions(msgs, "anthropic")

	if _, ok := msgs[1].Content[0].(provider.CompactionBlock); !ok {
		t.Fatalf("the live transcript now holds a %T — the blob is gone from the session, so "+
			"switching back to openai-codex can never replay it", msgs[1].Content[0])
	}
}

// The wiring, not just the helper.
//
// Every test above calls replaceForeignCompactions directly, and every one of
// them passes with the call REMOVED from Agent.buildRequest — which is the
// exact shape of the bug being fixed here: provider.ForeignCompactions was
// correct, tested, and had no production caller. A guard that only exercises
// the helper reproduces the defect it is meant to prevent.
//
// So this drives a real turn and reads what left for the wire.
// (captureClient is defined in agent_retry_test.go.)
func TestAForeignCompactionIsReplacedOnTheRequestItself(t *testing.T) {
	client := &captureClient{} // Name() == "capture"
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.SetMessages(compactedTranscript("openai-codex"))

	if err := a.Prompt(context.Background(), "and then?", nil, nil); err != nil {
		t.Fatal(err)
	}

	var sawBlob, sawNote bool
	for _, m := range client.lastReq.Messages {
		for _, c := range m.Content {
			switch v := c.(type) {
			case provider.CompactionBlock:
				sawBlob = true
			case provider.TextBlock:
				if strings.Contains(v.Text, "not available on this model") {
					sawNote = true
				}
			}
		}
	}
	if sawBlob {
		t.Error("a codex compaction blob reached the request built for provider \"capture\". Its " +
			"content switch has no arm for one and no default, so it is dropped with no trace and " +
			"the model answers from a conversation missing everything the blob replaced.")
	}
	if !sawNote {
		t.Error("the blob was removed from the request without leaving a note, so the gap is still silent")
	}
}

// Nothing to do costs nothing.
func TestATranscriptWithNoCompactionIsPassedThrough(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{
		provider.TextBlock{Text: "hi"},
	}}}
	if got := replaceForeignCompactions(msgs, "anthropic"); &got[0] != &msgs[0] {
		t.Error("a transcript with no compaction block got copied")
	}
}
