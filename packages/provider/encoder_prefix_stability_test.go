package provider

import (
	"encoding/json"
	"testing"
)

// The whole economics of a long conversation rests on one property: consecutive
// requests must share a byte-identical PREFIX and differ only by appending.
// terva's prefix-divergence guard checks that on provider.Request.Messages — but
// the codex builder rewrites the array BELOW that, through
// RepairOrphanedToolResults, MergeAdjacentSameRole and EnsureLeadingUserTurn,
// each a fold over the whole slice. A fold that rewrites an EARLIER element as
// the transcript grows would invalidate the cached prefix on every turn, and the
// guard — measuring the input to that fold — would report a clean append.
//
// These tests encode a growing transcript one message at a time and assert the
// encoded wire items really are append-only. This is the blind spot named in
// prefixwatch.go, measured rather than argued.

// growingTranscript is a realistic append-only session: user turn, assistant
// reply with a tool call, the tool result, another assistant turn, and so on —
// including the same-role adjacency that MergeAdjacentSameRole exists to fix.
func growingTranscript() []Message {
	return []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "start the task"}}},
		{Role: RoleAssistant, Content: []Content{
			TextBlock{Text: "reading"},
			ToolCallBlock{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
		}},
		{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "call_1", Content: []Content{TextBlock{Text: "contents of a"}}}}},
		{Role: RoleAssistant, Content: []Content{ToolCallBlock{ID: "call_2", Name: "read", Arguments: json.RawMessage(`{"path":"b"}`)}}},
		{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "call_2", Content: []Content{TextBlock{Text: "contents of b"}}}}},
		{Role: RoleAssistant, Content: []Content{TextBlock{Text: "done reading"}}},
		// Two user turns in a row — the adjacency the merge repairs. This is the
		// case most likely to rewrite an already-cached item.
		{Role: RoleUser, Content: []Content{TextBlock{Text: "also check c"}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "and d"}}},
		{Role: RoleAssistant, Content: []Content{TextBlock{Text: "checking"}}},
		{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "orphan", Content: []Content{TextBlock{Text: "no matching call"}}}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "status?"}}},
	}
}

// encodeItems renders each wire input item as canonical JSON, which is what a
// provider actually hashes for a prefix match.
func encodeItems(t *testing.T, c *codexClient, msgs []Message) []string {
	t.Helper()
	body, err := c.buildRequest(Request{
		Model:          "gpt-5.5",
		Messages:       msgs,
		System:         "system",
		PromptCacheKey: "sess-1",
	})
	if err != nil {
		t.Fatalf("buildRequest(%d msgs): %v", len(msgs), err)
	}
	out := make([]string, 0, len(body.Input))
	for _, item := range body.Input {
		b, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal input item: %v", err)
		}
		out = append(out, string(b))
	}
	return out
}

// TestCodexEncodedPrefixIsAppendOnly is the measurement. For every growth step,
// the previously encoded items must still be present, byte-identical, at the
// same positions — with the single documented exception below.
func TestCodexEncodedPrefixIsAppendOnly(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	full := growingTranscript()

	var prev []string
	var prevN int
	for n := 1; n <= len(full); n++ {
		cur := encodeItems(t, c, full[:n])
		if prev == nil {
			prev, prevN = cur, n
			continue
		}
		// The LAST item of the previous encoding may legitimately change: a
		// same-role append merges into it (MergeAdjacentSameRole). Everything
		// before it is prefix the provider has already cached, and must not move.
		stable := len(prev) - 1
		if len(cur) < stable {
			t.Fatalf("growing %d→%d messages SHRANK the encoded prefix: %d → %d items",
				prevN, n, len(prev), len(cur))
		}
		for i := 0; i < stable; i++ {
			if cur[i] != prev[i] {
				t.Errorf("growing %d→%d messages rewrote already-cached item %d of %d.\n"+
					"  before: %s\n  after:  %s\n"+
					"This is invisible to the prefix guard, which measures Request.Messages "+
					"BEFORE this encoding — so it would report a clean append while the "+
					"provider re-read the whole transcript.",
					prevN, n, i, len(prev), prev[i], cur[i])
			}
		}
		prev, prevN = cur, n
	}
}

// TestCodexSameRoleAppendOnlyDisturbsTheFinalItem bounds the one exception, so
// "the last item may change" stays a known, small cost rather than a licence.
// If merging ever reached further back, the test above fails; this one pins that
// the exception is real and is exactly one item.
func TestCodexSameRoleAppendOnlyDisturbsTheFinalItem(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	base := []Message{
		{Role: RoleUser, Content: []Content{TextBlock{Text: "one"}}},
		{Role: RoleAssistant, Content: []Content{TextBlock{Text: "two"}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "three"}}},
	}
	before := encodeItems(t, c, base)
	after := encodeItems(t, c, append(append([]Message(nil), base...),
		Message{Role: RoleUser, Content: []Content{TextBlock{Text: "four"}}}))

	if len(before) == 0 {
		t.Fatal("no encoded items")
	}
	for i := 0; i < len(before)-1; i++ {
		if after[i] != before[i] {
			t.Errorf("item %d moved on a same-role append: %s → %s", i, before[i], after[i])
		}
	}
	if after[len(before)-1] == before[len(before)-1] {
		t.Log("same-role append did not merge into the final item; the exception may be " +
			"narrower than documented")
	}
}

// TestCodexEncodingIsDeterministic rules out the other way an encoder can break
// caching: the same input producing different bytes on two calls (map iteration
// order reaching the wire, a timestamp, a generated id).
func TestCodexEncodingIsDeterministic(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	msgs := growingTranscript()
	first := encodeItems(t, c, msgs)
	for i := 0; i < 5; i++ {
		again := encodeItems(t, c, msgs)
		if len(again) != len(first) {
			t.Fatalf("encoding %d produced %d items, first produced %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("encoding is not deterministic at item %d:\n  %s\n  %s", j, first[j], again[j])
			}
		}
	}
}
