package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reasoning-shape census: every shape terva captures has exactly ONE
// serializer that will replay it, and every other serializer drops it.
//
// This is the same forced decision reasoning_wire_census_test.go makes about
// which wire a provider's reasoning KNOB rides, one layer down and about the
// reasoning BLOCK. It exists because a transcript outlives a /model switch, so
// every serializer is reached holding blocks it did not issue — and
// they are indistinguishable in Go, being summary-plus-opaque-string in every
// case. Before the tags, the Anthropic serializer was the only one that
// checked: Codex replayed an Anthropic signature as its own encrypted_content
// under an empty id, and the chat builder replayed Anthropic's verbatim
// chain-of-thought as the assistant's own prior words.
//
// 🪤 There is deliberately no "unknown means OpenAI-compatible" fallback. That
// default is exactly what hid kimi's misclassification for as long as it did.
// A shape added without an owner fails here rather than silently acquiring one.

// reasoningShapeOwners names, for every shape, the ONE serializer allowed to
// replay it. "" means no serializer may: the block is captured for the record
// and never handed back.
//
// Adding a shape means adding a row. That is the point — the compiler will not
// make you, and the wire will not tell you until a request fails.
var reasoningShapeOwners = []struct {
	shape string
	owner string // "anthropic" | "codex" | "chat" | "gemini" | "" (never replayed)
	block ReasoningBlock
}{
	{ReasoningShapeAnthropicThinking, "anthropic",
		ReasoningBlock{Summary: "weighed it", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking}},
	{ReasoningShapeAnthropicThinkingOpaque, "anthropic",
		ReasoningBlock{Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinkingOpaque}},
	{ReasoningShapeAnthropicRedacted, "anthropic",
		ReasoningBlock{Encrypted: "BLOB", Shape: ReasoningShapeAnthropicRedacted}},
	{ReasoningShapeOpenAIResponses, "codex",
		ReasoningBlock{ID: "rs_1", Summary: "précis", Encrypted: "OPAQUE", Shape: ReasoningShapeOpenAIResponses}},
	{ReasoningShapeOpenAIChat, "chat",
		ReasoningBlock{Summary: "deliberation", Shape: ReasoningShapeOpenAIChat}},
	// Gemini seals a text-only turn with a trailing signature part. The
	// SIGNATURE is replayed; the summary beside it never is.
	{ReasoningShapeGeminiThoughtSummary, "gemini",
		ReasoningBlock{Summary: "checking the handler", Encrypted: "GEMSIG", Shape: ReasoningShapeGeminiThoughtSummary}},
}

// replayedByAnthropic reports whether convertAnthContent emits anything for b.
func replayedByAnthropic(t *testing.T, b ReasoningBlock) bool {
	t.Helper()
	out, err := json.Marshal(convertAnthContent([]Content{b}, false))
	if err != nil {
		t.Fatal(err)
	}
	return string(out) != "[]"
}

// replayedByCodex reports whether the Responses builder emits a reasoning item
// for b. A tool call rides along so the turn is realistic; only the reasoning
// item is counted.
func replayedByCodex(t *testing.T, b ReasoningBlock) bool {
	t.Helper()
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-sol",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{b, TextBlock{Text: "answer"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range wire.Input {
		if _, ok := it.(codexReasoningItem); ok {
			return true
		}
	}
	return false
}

// replayedByChat reports whether the chat builder carries b's text back, by
// either route it has: reasoning_content beside a tool call, or promotion into
// the visible content of an otherwise-empty turn.
func replayedByChat(t *testing.T, b ReasoningBlock) bool {
	t.Helper()
	c := &openaiClient{name: "openai-compatible"}
	// Route 1: beside a tool call.
	withTool, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{b,
				ToolCallBlock{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{}`)}}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "call_1", Content: []Content{TextBlock{Text: "ok"}}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range withTool.Messages {
		if m.ReasoningContent != "" {
			return true
		}
	}
	// Route 2: promoted into content when it is the turn's only substance.
	alone, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{b}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "and?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range alone.Messages {
		if m.Role == "assistant" && m.Content != nil {
			return true
		}
	}
	return false
}

// replayedByGemini reports whether the Gemini builder emits a part for b. Only
// the signature is ever sent, so this looks for a thoughtSignature that is not
// riding a functionCall.
func replayedByGemini(t *testing.T, b ReasoningBlock) bool {
	t.Helper()
	parts := convertGemAssistantParts([]Content{b, TextBlock{Text: "answer"}}, true)
	for _, p := range parts {
		if p.ThoughtSignature != "" && p.FunctionCall == nil {
			return true
		}
	}
	return false
}

// The summary is a human-facing précis the model does not consume; replaying it
// would re-bill it as input on every following turn and buy nothing. Only the
// seal goes back, so a block carrying no seal is replayed nowhere.
func TestGeminiReplaysTheSignatureAndNeverTheSummary(t *testing.T) {
	withSig := ReasoningBlock{Summary: "the readable thinking", Encrypted: "GEMSIG", Shape: ReasoningShapeGeminiThoughtSummary}
	parts := convertGemAssistantParts([]Content{withSig, TextBlock{Text: "answer"}}, true)

	var sawSig bool
	for _, p := range parts {
		if strings.Contains(p.Text, "the readable thinking") {
			t.Errorf("replayed the thought summary as text: %q", p.Text)
		}
		if p.ThoughtSignature == "GEMSIG" {
			sawSig = true
		}
	}
	if !sawSig {
		t.Error("the signature never reached the wire")
	}

	summaryOnly := ReasoningBlock{Summary: "no seal here", Shape: ReasoningShapeGeminiThoughtSummary}
	if replayedByGemini(t, summaryOnly) {
		t.Error("a block with no signature produced a part; there is nothing to replay")
	}
}

func TestEveryReasoningShapeHasExactlyOneSerializer(t *testing.T) {
	if len(reasoningShapeOwners) == 0 {
		t.Fatal("no shapes listed — this census is vacuous")
	}
	for _, tc := range reasoningShapeOwners {
		t.Run(tc.shape, func(t *testing.T) {
			got := map[string]bool{
				"anthropic": replayedByAnthropic(t, tc.block),
				"codex":     replayedByCodex(t, tc.block),
				"chat":      replayedByChat(t, tc.block),
				"gemini":    replayedByGemini(t, tc.block),
			}
			for name, accepted := range got {
				switch {
				case name == tc.owner && !accepted:
					t.Errorf("%s is the owner of %q but dropped it — the block was captured and cannot be handed back",
						name, tc.shape)
				case name != tc.owner && accepted:
					t.Errorf("%s replayed %q, which belongs to %q. One provider's reasoning "+
						"sent to another is rejected outright (Anthropic, Codex) or leaks the "+
						"model's private deliberation into another vendor's context (chat).",
						name, tc.shape, tc.owner)
				}
			}
		})
	}
}

// An unknown shape — a future provider's, or a block written by a newer terva
// and read by an older one — must reach no serializer at all. This is the
// property that makes the absence of a default safe.
func TestAnUnknownReasoningShapeIsReplayedNowhere(t *testing.T) {
	b := ReasoningBlock{ID: "x", Summary: "from some future wire", Encrypted: "PAYLOAD", Shape: "someprovider.reasoning"}
	if replayedByAnthropic(t, b) {
		t.Error("anthropic replayed an unknown shape")
	}
	if replayedByCodex(t, b) {
		t.Error("codex replayed an unknown shape")
	}
	if replayedByChat(t, b) {
		t.Error("chat replayed an unknown shape")
	}
	if replayedByGemini(t, b) {
		t.Error("gemini replayed an unknown shape")
	}
}

// Non-vacuity for the two above. The helpers must be able to answer YES, or
// every assertion in this file passes by never detecting a replay at all.
func TestReplayProbesDetectARealReplay(t *testing.T) {
	if !replayedByAnthropic(t, ReasoningBlock{Summary: "x", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking}) {
		t.Error("the anthropic probe cannot see a replay it should")
	}
	if !replayedByCodex(t, ReasoningBlock{ID: "rs_1", Encrypted: "OPAQUE", Shape: ReasoningShapeOpenAIResponses}) {
		t.Error("the codex probe cannot see a replay it should")
	}
	if !replayedByChat(t, ReasoningBlock{Summary: "x", Shape: ReasoningShapeOpenAIChat}) {
		t.Error("the chat probe cannot see a replay it should")
	}
	if !replayedByGemini(t, ReasoningBlock{Encrypted: "SIG", Shape: ReasoningShapeGeminiThoughtSummary}) {
		t.Error("the gemini probe cannot see a replay it should")
	}
}

// The legacy rule, stated as behavior rather than as an implementation detail.
// Untagged blocks are a frozen population — every capture site tags at the
// source now — so this only has to be right about what is already on disk.
func TestLegacyUntaggedReasoningIsAttributedByItsPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ReasoningBlock
		want string
	}{
		// The one that matters: a Codex reasoning item whose replay is
		// MANDATORY. Losing it makes the backend reject the next tool call in
		// a resumed session, so an untagged payload must land here.
		{"codex item with an id", ReasoningBlock{ID: "rs_1", Encrypted: "OPAQUE"}, ReasoningShapeOpenAIResponses},
		{"codex item, id lost", ReasoningBlock{Encrypted: "OPAQUE"}, ReasoningShapeOpenAIResponses},
		// Summary-only is prose from a chat-completions provider (or a Gemini
		// summary, which is inert either way). Misfiling it costs some text.
		{"chat reasoning_content", ReasoningBlock{Summary: "thinking out loud"}, ReasoningShapeOpenAIChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeLegacyReasoningShape(tc.in).Shape; got != tc.want {
				t.Errorf("Shape = %q, want %q", got, tc.want)
			}
		})
	}
}

// A tagged block is never re-attributed. Without this the back-fill could
// quietly overwrite a real tag — and since Anthropic blocks have carried one
// since capture for them existed, that would hand Anthropic signatures to the
// Codex serializer, which is the exact bug the tags prevent.
func TestNormalizeLeavesATaggedBlockAlone(t *testing.T) {
	for _, tc := range reasoningShapeOwners {
		if got := NormalizeLegacyReasoningShape(tc.block); got != tc.block {
			t.Errorf("%q was rewritten to %q", tc.shape, got.Shape)
		}
	}
}
