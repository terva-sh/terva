package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// A compaction summary must go back to the backend exactly as it arrived.
//
// The blob is the backend's own encoding of the turns it compacted away. Terva
// cannot read it, cannot rebuild it, and holds no other copy of that context —
// so a serializer that re-typed it, truncated it, or dropped it would destroy
// the conversation's history while leaving a transcript that looks intact.
func TestCodexReplaysACompactionSummaryVerbatim(t *testing.T) {
	const blob = "gAAAAABqcmYNtDz9qaagugIoj1AiwgEXQPO4PGoHSPr-Epfo60MD1boGFgQy5TM3=="
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-terra",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "earlier"}}},
			{Role: RoleAssistant, Content: []Content{CompactionBlock{ID: "cmp_abc123", Encrypted: blob}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "now"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	// The wire type is compaction_summary. The published write-ups say
	// "compaction"; a live probe of the backend says otherwise, and the probe
	// is what the backend actually accepts.
	if !strings.Contains(got, `"type":"compaction_summary"`) {
		t.Errorf("no compaction_summary item in the request:\n%s", got)
	}
	if strings.Contains(got, `"type":"compaction"`) {
		t.Errorf(`item typed "compaction" — the backend's name is "compaction_summary":\n%s`, got)
	}
	if !strings.Contains(got, `"id":"cmp_abc123"`) {
		t.Errorf("compaction item lost its id:\n%s", got)
	}
	if !strings.Contains(got, blob) {
		t.Errorf("the encrypted blob was altered or dropped; it must round-trip byte for byte:\n%s", got)
	}
}

// The block must not disturb the ordering of the transcript around it. The
// compaction stands where the turns it replaced stood, so an item emitted out
// of order would hand the model its history rearranged.
func TestCompactionSummaryKeepsItsPlaceInTheInput(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-terra",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "FIRST"}}},
			{Role: RoleAssistant, Content: []Content{CompactionBlock{ID: "cmp_1", Encrypted: "BLOB"}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "LAST"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(wire)
	got := string(b)
	first, blob, last := strings.Index(got, "FIRST"), strings.Index(got, "BLOB"), strings.Index(got, "LAST")
	if first < 0 || blob < 0 || last < 0 {
		t.Fatalf("one of the three items is missing entirely:\n%s", got)
	}
	if !(first < blob && blob < last) {
		t.Errorf("input order is user/compaction/user but serialized as %d/%d/%d:\n%s", first, blob, last, got)
	}
}

// Replay eligibility is asked ONCE, above the serializers.
//
// Every provider's content switch is a type switch with no default arm, so a
// block it does not recognize is dropped silently — and dropping a compaction
// blob is amnesia, not degradation: it is the only encoding of the assistant
// turns it replaced, so the model gets a conversation that reads continuous and
// is missing half its history. Asking here is what turns that into a decision.
func TestForeignCompactionsFindsWhatAProviderCannotReplay(t *testing.T) {
	mine := Message{Role: RoleAssistant, Content: []Content{
		CompactionBlock{ID: "cmp_1", Encrypted: "blob", Provider: "openai-codex"}}}
	theirs := Message{Role: RoleAssistant, Content: []Content{
		CompactionBlock{ID: "cmp_2", Encrypted: "blob", Provider: "anthropic"}}}
	// Written before provenance existed. Guessing it belongs to whoever is
	// asking is the guess that loses data, so it is foreign to everyone.
	legacy := Message{Role: RoleAssistant, Content: []Content{
		CompactionBlock{ID: "cmp_3", Encrypted: "blob"}}}
	plain := Message{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}

	msgs := []Message{plain, mine, theirs, legacy}
	got := ForeignCompactions(msgs, "openai-codex")
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("want the anthropic block and the provenance-less one (indices 2,3), got %v", got)
	}
	if own := ForeignCompactions([]Message{plain, mine}, "openai-codex"); len(own) != 0 {
		t.Errorf("a provider's own blob is replayable, got %v", own)
	}
	if none := ForeignCompactions([]Message{plain}, "anthropic"); len(none) != 0 {
		t.Errorf("a transcript with no compaction has nothing foreign, got %v", none)
	}
}
