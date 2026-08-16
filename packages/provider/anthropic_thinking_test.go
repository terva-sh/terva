package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Anthropic thinking, both directions: off the wire into a ReasoningBlock, and
// back onto the wire unchanged.
//
// The signature is what makes this different from every other provider terva
// captures reasoning from. It is a seal over the thinking TEXT, not an opaque
// payload sitting beside it, so the two halves are one thing — which is why the
// capture drops a half-block and the replay refuses to send one.

// anthThinkingFrames is a stream carrying a thinking block (delivered as two
// text deltas then its signature), then an answer.
const anthThinkingFrames = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The config is read twice"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":", so the second read wins."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCgIYAhIM1gbc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Fixed."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}

event: message_stop
data: {"type":"message_stop"}

`

// runAnthStream drives runStream over a canned SSE body, as a budget-thinking
// model. Adaptive models go through runAnthStreamAs, which is otherwise the
// same call — see TestAdaptiveFramesCaptureTheSameUnderEitherModel.
func runAnthStream(t *testing.T, frames string) (deltas string, text string, msg Message) {
	t.Helper()
	d, tx, m, _ := runAnthStreamAs(t, frames, "claude-opus-4-5")
	return d, tx, m
}

// runAnthStreamAs is runAnthStream with the model spelled out and usage
// returned. The model reaches runStream only through the catalog lookup that
// prices the turn, so it is the knob for "does capture depend on which model
// answered" — which it must not.
func runAnthStreamAs(t *testing.T, frames, model string) (deltas string, text string, msg Message, usage Usage) {
	t.Helper()
	c := NewAnthropic("k", "").(*anthropicClient)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(frames)), Header: http.Header{}}
	out := make(chan Event, 64)
	go c.runStream(context.Background(), resp, Request{Model: model}, out)

	var d, tx strings.Builder
	for ev := range out {
		switch e := ev.(type) {
		case EventReasoningDelta:
			d.WriteString(e.Delta)
		case EventTextDelta:
			tx.WriteString(e.Delta)
		case EventUsage:
			usage = e.Usage
		case EventDone:
			msg = e.Message
		}
	}
	return d.String(), tx.String(), msg, usage
}

func reasoningBlocks(m Message) []ReasoningBlock {
	var out []ReasoningBlock
	for _, c := range m.Content {
		if rb, ok := c.(ReasoningBlock); ok {
			out = append(out, rb)
		}
	}
	return out
}

// The live half and the recorded half have to agree, and neither may leak into
// the reply — thinking rendered as assistant text would put the model's private
// deliberation back in its own mouth on the following turn.
func TestAnthropicThinkingIsCapturedLiveAndOnTheMessage(t *testing.T) {
	deltas, text, msg := runAnthStream(t, anthThinkingFrames)

	const want = "The config is read twice, so the second read wins."
	if deltas != want {
		t.Errorf("live reasoning deltas = %q, want %q", deltas, want)
	}
	if text != "Fixed." {
		t.Errorf("answer text = %q, want %q", text, "Fixed.")
	}

	rbs := reasoningBlocks(msg)
	if len(rbs) != 1 {
		t.Fatalf("got %d reasoning blocks, want 1: %+v", len(rbs), msg.Content)
	}
	if rbs[0].Summary != want {
		t.Errorf("ReasoningBlock.Summary = %q, want %q", rbs[0].Summary, want)
	}
	// The signature is accumulated, never streamed: it is not reasoning to
	// read, and emitting it would put base64 in the middle of the live line.
	if rbs[0].Encrypted != "EqQBCgIYAhIM1gbc" {
		t.Errorf("ReasoningBlock.Encrypted = %q, want the signature", rbs[0].Encrypted)
	}
	if strings.Contains(deltas, "EqQBCgIYAhIM1gbc") {
		t.Error("the signature was emitted as a reasoning delta; it is not readable reasoning")
	}
	if rbs[0].Shape != ReasoningShapeAnthropicThinking {
		t.Errorf("Shape = %q, want %q", rbs[0].Shape, ReasoningShapeAnthropicThinking)
	}

	// Thinking must not also arrive as reply text.
	for _, c := range msg.Content {
		if tb, ok := c.(TextBlock); ok && strings.Contains(tb.Text, "read twice") {
			t.Errorf("thinking leaked into a TextBlock: %q", tb.Text)
		}
	}
	// Order matters on the way back out: Anthropic wants the thinking block
	// ahead of what it produced, and assembleMsg preserves emission order.
	if _, ok := msg.Content[0].(ReasoningBlock); !ok {
		t.Errorf("first block is %T, want the ReasoningBlock to lead", msg.Content[0])
	}
}

// A stream cut after the thinking text but before the signature leaves a block
// that can never be replayed — Anthropic verifies the pair. Carrying it would
// turn one truncated response into a 400 on every following turn, so it is
// dropped at assembly.
func TestAnthropicThinkingWithoutItsSignatureIsDropped(t *testing.T) {
	cut := strings.Split(anthThinkingFrames, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\"")
	if len(cut) != 2 {
		t.Fatalf("fixture no longer contains exactly one signature_delta frame (%d parts)", len(cut))
	}
	// Everything up to the signature frame, then straight to the answer.
	frames := cut[0] + "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	deltas, _, msg := runAnthStream(t, frames)

	// The live line still ran: display never depended on the signature.
	if deltas == "" {
		t.Error("no reasoning deltas; the text before the cut should still have been shown")
	}
	if rbs := reasoningBlocks(msg); len(rbs) != 0 {
		t.Errorf("kept an unreplayable thinking block: %+v", rbs)
	}
}

const anthRedactedFrames = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}

event: message_stop
data: {"type":"message_stop"}

`

// Redacted thinking arrives whole and encrypted: nothing to show, everything to
// keep. It is tagged distinctly because its replay shape differs, and that
// cannot be recovered from the block's contents later.
func TestAnthropicRedactedThinkingIsKeptButNotShown(t *testing.T) {
	deltas, text, msg := runAnthStream(t, anthRedactedFrames)

	if deltas != "" {
		t.Errorf("redacted thinking produced live deltas %q; there is no readable text to show", deltas)
	}
	if text != "Done." {
		t.Errorf("answer text = %q, want %q", text, "Done.")
	}
	rbs := reasoningBlocks(msg)
	if len(rbs) != 1 {
		t.Fatalf("got %d reasoning blocks, want 1: %+v", len(rbs), msg.Content)
	}
	if rbs[0].Encrypted != "EmwKAhgBEgy3va3" {
		t.Errorf("Encrypted = %q, want the redacted payload", rbs[0].Encrypted)
	}
	if rbs[0].Summary != "" {
		t.Errorf("Summary = %q, want empty — a redacted block has no readable half", rbs[0].Summary)
	}
	if rbs[0].Shape != ReasoningShapeAnthropicRedacted {
		t.Errorf("Shape = %q, want %q", rbs[0].Shape, ReasoningShapeAnthropicRedacted)
	}
}

// convertAnthContent is the replay side. Serializing to JSON rather than
// asserting on the Go structs is deliberate: the field names ARE the contract,
// and a struct comparison would pass with `thinking` spelled `text`.
func anthReplayJSON(t *testing.T, blocks ...Content) string {
	t.Helper()
	b, err := json.Marshal(convertAnthContent(blocks, false))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnthropicThinkingReplaysVerbatim(t *testing.T) {
	got := anthReplayJSON(t,
		ReasoningBlock{Summary: "weighing it", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking},
		ReasoningBlock{Encrypted: "BLOB", Shape: ReasoningShapeAnthropicRedacted},
		TextBlock{Text: "answer"},
	)
	want := `[{"type":"thinking","thinking":"weighing it","signature":"SIG"},` +
		`{"type":"redacted_thinking","data":"BLOB"},` +
		`{"type":"text","text":"answer"}]`
	if got != want {
		t.Errorf("replay mismatch:\n got %s\nwant %s", got, want)
	}
}

// 🪤 The one that motivated the Shape tag. A transcript outlives a /model
// switch, so this serializer is reached holding another provider's reasoning.
// Codex blocks look identical in Go — Summary plus an opaque string — and
// dressing one up as an Anthropic thinking block sends a signature Anthropic
// never issued, which fails the whole request on every turn that follows.
func TestForeignReasoningIsNeverReplayedToAnthropic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block ReasoningBlock
	}{
		{"codex item", ReasoningBlock{ID: "rs_1", Summary: "deliberating", Encrypted: "OPAQUE"}},
		{"gemini thought summary", ReasoningBlock{Summary: "checking the handler"}},
		{"unknown shape", ReasoningBlock{Summary: "x", Encrypted: "y", Shape: "openai.reasoning"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthReplayJSON(t, tc.block); got != "[]" {
				t.Errorf("serialized foreign reasoning to the Anthropic wire: %s", got)
			}
		})
	}
}

// Half a pair is worse than none. A block whose text was blanked for display-only
// recording still carries a signature, and the signature no longer seals
// anything — sending it earns a 400 in place of a turn.
func TestAnthropicThinkingStrippedOfItsTextIsNotReplayed(t *testing.T) {
	if got := anthReplayJSON(t,
		ReasoningBlock{Summary: "", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking},
	); got != "[]" {
		t.Errorf("replayed a signature with no text to seal: %s", got)
	}
	// The mirror case: text with no signature is equally unsendable.
	if got := anthReplayJSON(t,
		ReasoningBlock{Summary: "orphaned", Encrypted: "", Shape: ReasoningShapeAnthropicThinking},
	); got != "[]" {
		t.Errorf("replayed thinking with no signature: %s", got)
	}
}

// Non-vacuity for the two tests above: the same helper on a WHOLE block must
// produce a block. Without this, a convertAnthContent that dropped every
// ReasoningBlock — today's behavior, and the regression this feature is one
// edit away from — would pass both of them.
func TestAnthropicReplayHelperEmitsWhenTheBlockIsWhole(t *testing.T) {
	if got := anthReplayJSON(t,
		ReasoningBlock{Summary: "kept", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking},
	); got == "[]" {
		t.Fatal("a whole thinking block produced nothing; the drop-tests above are passing vacuously")
	}
}

// End of the road: a full request body built from a transcript that already
// contains a captured thinking block. The unit tests above cover
// convertAnthContent in isolation; this one covers everything between a stored
// message and the bytes on the wire, which is where a filter that strips
// assistant content or a role mapping that reorders it would show up.
//
// The assertion is POSITIONAL on purpose. Anthropic requires the assistant turn
// preceding a tool result to LEAD with its thinking, so "the block is somewhere
// in the array" is not the property that matters.
func TestBuildRequestReplaysThinkingAheadOfTheToolCall(t *testing.T) {
	c := &anthropicClient{}
	wire, err := c.buildRequest(Request{
		Model: "claude-opus-4-5",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "fix it"}}},
			{Role: RoleAssistant, Content: []Content{
				ReasoningBlock{Summary: "the second read wins", Encrypted: "SIG", Shape: ReasoningShapeAnthropicThinking},
				ToolCallBlock{ID: "tu_1", Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`)},
			}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "tu_1", Content: []Content{TextBlock{Text: "ok"}}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("assistant carries %d blocks, want thinking + tool_use: %#v", len(assistant.Content), assistant.Content)
	}
	th, ok := assistant.Content[0].(anthThinkingBlock)
	if !ok {
		t.Fatalf("assistant turn leads with %T, want the thinking block", assistant.Content[0])
	}
	if th.Thinking != "the second read wins" || th.Signature != "SIG" {
		t.Errorf("thinking block was altered in transit: %+v", th)
	}
	if _, ok := assistant.Content[1].(anthToolUseBlock); !ok {
		t.Errorf("second block is %T, want the tool_use", assistant.Content[1])
	}
}
