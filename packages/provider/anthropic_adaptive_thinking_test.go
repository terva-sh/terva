package provider

import (
	"strings"
	"testing"
)

// Adaptive thinking, the half of the Anthropic capture path that fixtures
// invented from the budget path could not have described.
//
// 🪤 An adaptive-thinking model (Opus 4.7+, Sonnet 5) does NOT stream readable
// thinking. It sends one thinking_delta carrying the EMPTY STRING and then a
// real signature: the reasoning is withheld, but signed and expected back. The
// capture required both halves to be non-empty, so it threw every one of those
// blocks away — and the model had spent 254 of its 258 output tokens producing
// it.
//
// The frames below are a live sonnet-5 response, verbatim except for the
// signature, which is truncated (it is 808 characters and nothing here reads
// it). The stray whitespace inside the data payloads and the ping frame are
// Anthropic's, kept because a parser that only ever sees tidy fixtures is a
// parser that has not been tested.
const anthAdaptiveThinkingFrames = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_011Ce591zY6g7eJH3rJpjbk8","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":90,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}} }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""} }

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}            }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"ErMFCokBCBAYAipAo4frv9G2Pv/4jlmkj3XF68fFSt4UpC5ELFEOVnk0QgBHjbwTmFBrkQGuLwxLR4AO"}   }

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}             }

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"405"}   }

event: content_block_stop
data: {"type":"content_block_stop","index":1              }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":90,"output_tokens":258,"output_tokens_details":{"thinking_tokens":254}}  }

event: message_stop
data: {"type":"message_stop"              }
`

const anthAdaptiveSignature = "ErMFCokBCBAYAipAo4frv9G2Pv/4jlmkj3XF68fFSt4UpC5ELFEOVnk0QgBHjbwTmFBrkQGuLwxLR4AO"

// The block must survive capture, and it must be tagged as the textless kind.
// Both halves matter: keeping it makes replay possible, and the tag is what
// keeps it distinguishable from a thinking block someone stripped.
func TestAdaptiveThinkingIsCapturedDespiteHavingNoText(t *testing.T) {
	deltas, text, msg, _ := runAnthStreamAs(t, anthAdaptiveThinkingFrames, "claude-sonnet-5")

	if text != "405" {
		t.Errorf("answer text = %q, want %q", text, "405")
	}
	// Nothing readable arrived, so nothing may be displayed. An empty
	// thinking_delta must not produce a reasoning line at all.
	if deltas != "" {
		t.Errorf("live reasoning deltas = %q; the model sent no readable thinking", deltas)
	}

	rbs := reasoningBlocks(msg)
	if len(rbs) != 1 {
		t.Fatalf("got %d reasoning blocks, want 1 — a signed block was discarded: %+v", len(rbs), msg.Content)
	}
	if rbs[0].Shape != ReasoningShapeAnthropicThinkingOpaque {
		t.Errorf("Shape = %q, want %q", rbs[0].Shape, ReasoningShapeAnthropicThinkingOpaque)
	}
	if rbs[0].Summary != "" {
		t.Errorf("Summary = %q, want empty — Anthropic withheld the text", rbs[0].Summary)
	}
	if rbs[0].Encrypted != anthAdaptiveSignature {
		t.Errorf("Encrypted = %q, want the signature", rbs[0].Encrypted)
	}
	// Same ordering contract as the budget path: thinking leads the turn.
	if _, ok := msg.Content[0].(ReasoningBlock); !ok {
		t.Errorf("first block is %T, want the ReasoningBlock to lead", msg.Content[0])
	}
}

// The discriminator, stated directly: what separates the two thinking shapes is
// whether any text ever arrived, and that is decided at capture because nothing
// downstream can recover it. Two streams differing ONLY in the thinking_delta
// payload must produce two different shapes.
func TestThinkingShapeIsDecidedByWhetherTextArrived(t *testing.T) {
	withText := strings.Replace(anthAdaptiveThinkingFrames,
		`{"type":"thinking_delta","thinking":""}`,
		`{"type":"thinking_delta","thinking":"405 works because floor sums to 100"}`, 1)
	if withText == anthAdaptiveThinkingFrames {
		t.Fatal("fixture no longer contains the empty thinking_delta this test edits")
	}

	_, _, opaque, _ := runAnthStreamAs(t, anthAdaptiveThinkingFrames, "claude-sonnet-5")
	deltas, _, texted, _ := runAnthStreamAs(t, withText, "claude-sonnet-5")

	ob, tb := reasoningBlocks(opaque), reasoningBlocks(texted)
	if len(ob) != 1 || len(tb) != 1 {
		t.Fatalf("want one block each, got %d and %d", len(ob), len(tb))
	}
	if ob[0].Shape != ReasoningShapeAnthropicThinkingOpaque {
		t.Errorf("textless block tagged %q, want %q", ob[0].Shape, ReasoningShapeAnthropicThinkingOpaque)
	}
	if tb[0].Shape != ReasoningShapeAnthropicThinking {
		t.Errorf("block with text tagged %q, want %q", tb[0].Shape, ReasoningShapeAnthropicThinking)
	}
	// And the readable one is still displayed, which is the behavior the
	// opaque case must not have quietly taken away.
	if deltas == "" {
		t.Error("a thinking block WITH text produced no live deltas")
	}
}

// Capture must not depend on which model answered. The stream parser never sees
// the request, so this should be free — but "should be free" is the reasoning
// that left the adaptive path untested in the first place, and the model id
// does reach runStream.
func TestAdaptiveFramesCaptureTheSameUnderEitherModel(t *testing.T) {
	_, _, asAdaptive, _ := runAnthStreamAs(t, anthAdaptiveThinkingFrames, "claude-sonnet-5")
	_, _, asBudget, _ := runAnthStreamAs(t, anthAdaptiveThinkingFrames, "claude-opus-4-5")

	a, b := reasoningBlocks(asAdaptive), reasoningBlocks(asBudget)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("want one block each, got %d and %d", len(a), len(b))
	}
	if a[0] != b[0] {
		t.Errorf("capture differs by model:\n adaptive %+v\n budget   %+v", a[0], b[0])
	}
}

// Replay: the empty text is what Anthropic signed, so the empty text is what
// goes back. Verified live — a captured {thinking:"", signature} was accepted
// and the conversation continued.
func TestAdaptiveThinkingReplaysWithItsEmptyText(t *testing.T) {
	got := anthReplayJSON(t,
		ReasoningBlock{Summary: "", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinkingOpaque},
		TextBlock{Text: "405"},
	)
	want := `[{"type":"thinking","thinking":"","signature":"SIG"},{"type":"text","text":"405"}]`
	if got != want {
		t.Errorf("replay mismatch:\n got %s\nwant %s", got, want)
	}
}

// 🪤 The reason the shape exists rather than a Summary == "" test. These two
// blocks are byte-identical in Go apart from the tag, and they must serialize
// DIFFERENTLY: one is reasoning Anthropic withheld and will verify, the other
// is reasoning terva blanked and whose signature now seals nothing.
//
// Get this wrong in the permissive direction and every recording-off turn 400s;
// get it wrong in the strict direction and adaptive models silently lose their
// thinking, which is the bug this file was written for.
func TestBlankedThinkingAndWithheldThinkingDivergeOnReplay(t *testing.T) {
	const sig = "SIG"
	blanked := ReasoningBlock{Summary: "", Encrypted: sig, Shape: ReasoningShapeAnthropicThinking}
	withheld := ReasoningBlock{Summary: "", Encrypted: sig, Shape: ReasoningShapeAnthropicThinkingOpaque}

	if blanked.Summary != withheld.Summary || blanked.Encrypted != withheld.Encrypted {
		t.Fatal("fixture drifted: the two blocks must differ ONLY by Shape for this to prove anything")
	}
	if got := anthReplayJSON(t, blanked); got != "[]" {
		t.Errorf("replayed a blanked block whose signature seals text that is gone: %s", got)
	}
	if got := anthReplayJSON(t, withheld); got == "[]" {
		t.Error("dropped a withheld-thinking block; this is the adaptive data loss returning")
	}
}

// A stream cut before the signature is still unreplayable, textless or not.
// Relaxing the text requirement must not have relaxed this one — the signature
// is the half that was always load-bearing.
func TestAdaptiveThinkingWithoutItsSignatureIsStillDropped(t *testing.T) {
	cut := strings.Replace(anthAdaptiveThinkingFrames,
		`{"type":"signature_delta","signature":"`+anthAdaptiveSignature+`"}`,
		`{"type":"signature_delta","signature":""}`, 1)
	if cut == anthAdaptiveThinkingFrames {
		t.Fatal("fixture no longer contains the signature_delta this test blanks")
	}
	_, text, msg, _ := runAnthStreamAs(t, cut, "claude-sonnet-5")
	if text != "405" {
		t.Errorf("answer text = %q; the rest of the turn should be unaffected", text)
	}
	if rbs := reasoningBlocks(msg); len(rbs) != 0 {
		t.Errorf("kept a block with neither text nor signature: %+v", rbs)
	}
}

// The thinking tokens were invisible: Anthropic was documented as the provider
// that never breaks reasoning out, and adaptive models do. A turn that spent
// 254 of 258 output tokens thinking reported "not measured".
func TestAdaptiveThinkingTokensAreRead(t *testing.T) {
	_, _, _, usage := runAnthStreamAs(t, anthAdaptiveThinkingFrames, "claude-sonnet-5")

	if usage.OutputTokens != 258 {
		t.Errorf("OutputTokens = %d, want 258", usage.OutputTokens)
	}
	if !usage.ReasoningTokensKnown {
		t.Error("ReasoningTokensKnown = false, but the response carried output_tokens_details")
	}
	if usage.ReasoningTokens != 254 {
		t.Errorf("ReasoningTokens = %d, want 254", usage.ReasoningTokens)
	}
	// A subset of OutputTokens, never a bucket beside it.
	if usage.ReasoningTokens > usage.OutputTokens {
		t.Errorf("ReasoningTokens %d exceeds OutputTokens %d", usage.ReasoningTokens, usage.OutputTokens)
	}
}

// The other half of that flag: a budget-model response carries no
// output_tokens_details at all, and absence must stay absence rather than
// becoming a measured zero. Without this the fix above would report every
// non-adaptive Anthropic turn as "measured: 0 thinking tokens".
func TestBudgetThinkingReportsReasoningTokensUnknown(t *testing.T) {
	_, _, _, usage := runAnthStreamAs(t, anthThinkingFrames, "claude-opus-4-5")

	if usage.ReasoningTokensKnown {
		t.Error("ReasoningTokensKnown = true, but this response broke out no thinking count")
	}
	if usage.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d, want 0", usage.ReasoningTokens)
	}
}

// End of the road for the adaptive path: an adaptive model, a transcript
// carrying a withheld-thinking block, and the request terva actually builds.
// The request shape and the replay have to be right in the SAME body — they are
// independently correct today and were never checked together.
func TestAdaptiveRequestReplaysWithheldThinkingAheadOfTheAnswer(t *testing.T) {
	c := &anthropicClient{}
	wire, err := c.buildRequest(Request{
		Model:        "claude-sonnet-5",
		Reasoning:    "high",
		ReasoningSet: true,
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "how many trailing zeros"}}},
			{Role: RoleAssistant, Content: []Content{
				ReasoningBlock{Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinkingOpaque},
				TextBlock{Text: "405"},
			}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "and 101?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Request side: adaptive, effort-steered, and carrying no budget.
	if wire.Thinking == nil || wire.Thinking.Type != "adaptive" {
		t.Fatalf("thinking = %+v, want type adaptive", wire.Thinking)
	}
	if wire.Thinking.BudgetTokens != 0 {
		t.Errorf("adaptive request carries budget_tokens %d; the model rejects it", wire.Thinking.BudgetTokens)
	}
	if wire.OutputConfig == nil || wire.OutputConfig.Effort != "high" {
		t.Errorf("output_config = %+v, want effort high", wire.OutputConfig)
	}

	// Replay side: the block came back, leading the assistant turn.
	var assistant *anthMessage
	for i := range wire.Messages {
		if wire.Messages[i].Role == "assistant" {
			assistant = &wire.Messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message in the built request")
	}
	if len(assistant.Content) != 2 {
		t.Fatalf("assistant carries %d blocks, want thinking + text: %#v", len(assistant.Content), assistant.Content)
	}
	th, ok := assistant.Content[0].(anthThinkingBlock)
	if !ok {
		t.Fatalf("assistant turn leads with %T, want the thinking block", assistant.Content[0])
	}
	if th.Signature != "SIG" || th.Thinking != "" {
		t.Errorf("withheld thinking was altered in transit: %+v", th)
	}
}
