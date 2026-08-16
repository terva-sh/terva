package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Anthropic breaks the assumption the display/record split was built on.
//
// A Codex summary only exists because terva asked for one, so "don't record it"
// is answered by not asking. Anthropic sends thinking the moment thinking is
// enabled — nobody can decline it — and its signature seals the text, so the
// readable half and the replayable half are one object. Recording off therefore
// has to drop the block rather than quiet it, and that is a real cost: the block
// is what Anthropic wants replayed beside a tool call.

// anthropicShapedClient finishes a turn with the block set an Anthropic turn
// actually produces: signed thinking, an encrypted redacted block, and text.
type anthropicShapedClient struct{}

func (c *anthropicShapedClient) Name() string { return "anthropic-shaped-fake" }

func (c *anthropicShapedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventReasoningDelta{Delta: "Two callers read this"}
		out <- provider.EventTextDelta{Delta: "done"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.ReasoningBlock{
					Summary:   "Two callers read this",
					Encrypted: "SIG",
					Shape:     provider.ReasoningShapeAnthropicThinking,
				},
				provider.ReasoningBlock{
					Encrypted: "BLOB",
					Shape:     provider.ReasoningShapeAnthropicRedacted,
				},
				provider.TextBlock{Text: "done"},
			},
		}}
	}()
	return out, nil
}

// reasoningBlocksOf returns every ReasoningBlock on the trailing assistant
// message, in order.
func reasoningBlocksOf(t *testing.T, a *Agent) []provider.ReasoningBlock {
	t.Helper()
	msgs := a.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	var out []provider.ReasoningBlock
	for _, c := range msgs[len(msgs)-1].Content {
		if rb, ok := c.(provider.ReasoningBlock); ok {
			out = append(out, rb)
		}
	}
	return out
}

// runAnthropicShaped drives one turn and returns the live reasoning text.
func runAnthropicShaped(t *testing.T, tune func(*Agent)) (*Agent, string) {
	t.Helper()
	a := NewAgent(&anthropicShapedClient{}, "fake-model", "system", Registry{})
	tune(a)
	var live string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if d, ok := ev.(EvReasoningDelta); ok {
			live += d.Delta
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return a, live
}

// The default. Nobody asked to record thinking, so the model's unabridged
// deliberation must not land in the session file — which for this provider
// means the block goes, because a blanked one is only a signature sealing
// nothing.
func TestAnthropicThinkingIsNotRecordedByDefault(t *testing.T) {
	a, _ := runAnthropicShaped(t, func(*Agent) {})

	for _, rb := range reasoningBlocksOf(t, a) {
		if rb.Shape == provider.ReasoningShapeAnthropicThinking {
			t.Errorf("recorded thinking with recording off: %+v", rb)
		}
	}
	// The answer is untouched — dropping reasoning must not eat the turn.
	if msgs := a.Messages(); len(msgs) == 0 || len(msgs[len(msgs)-1].Content) == 0 {
		t.Fatal("the assistant message was emptied")
	}
}

// Display on, recording still off: the thinking is watchable and still absent
// from the record. This is the case the split exists for.
func TestAnthropicThinkingIsShownWithoutBeingRecorded(t *testing.T) {
	a, live := runAnthropicShaped(t, func(a *Agent) { a.SetShowReasoning(true) })

	if live != "Two callers read this" {
		t.Errorf("live reasoning = %q, want the thinking text", live)
	}
	for _, rb := range reasoningBlocksOf(t, a) {
		if rb.Shape == provider.ReasoningShapeAnthropicThinking {
			t.Errorf("display-only recorded the thinking: %+v", rb)
		}
	}
}

// Recording on: the block survives WHOLE. Text and signature together or the
// replay is worthless, so this asserts both halves rather than just presence.
func TestAnthropicThinkingIsRecordedIntactWhenAsked(t *testing.T) {
	a, _ := runAnthropicShaped(t, func(a *Agent) { a.SetReasoningSummary("auto") })

	blocks := reasoningBlocksOf(t, a)
	var thinking *provider.ReasoningBlock
	for i := range blocks {
		if blocks[i].Shape == provider.ReasoningShapeAnthropicThinking {
			thinking = &blocks[i]
		}
	}
	if thinking == nil {
		t.Fatal("recording is on and the thinking block is gone")
	}
	if thinking.Summary != "Two callers read this" {
		t.Errorf("Summary = %q, want the thinking text", thinking.Summary)
	}
	if thinking.Encrypted != "SIG" {
		t.Errorf("Encrypted = %q, want the signature — text without its seal cannot be replayed", thinking.Encrypted)
	}
}

// Redacted thinking has no readable half to withhold, and it is pure replay
// state. Recording off must not take it: dropping it would discard something
// that costs the user nothing in privacy and everything in replayability.
func TestRedactedThinkingSurvivesRecordingOff(t *testing.T) {
	a, _ := runAnthropicShaped(t, func(*Agent) {})

	found := false
	for _, rb := range reasoningBlocksOf(t, a) {
		if rb.Shape == provider.ReasoningShapeAnthropicRedacted {
			found = true
			if rb.Encrypted != "BLOB" {
				t.Errorf("redacted payload = %q, want BLOB", rb.Encrypted)
			}
		}
	}
	if !found {
		t.Error("recording-off dropped the redacted block; it has no text to hide")
	}
}

// adaptiveShapedClient ends a turn the way an adaptive-thinking model does:
// a signed thinking block whose readable text Anthropic withheld, then the
// answer. There is no live reasoning to emit because none was sent.
type adaptiveShapedClient struct{}

func (c *adaptiveShapedClient) Name() string { return "adaptive-shaped-fake" }

func (c *adaptiveShapedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventTextDelta{Delta: "405"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.ReasoningBlock{
					Encrypted: "SIG",
					Shape:     provider.ReasoningShapeAnthropicThinkingOpaque,
				},
				provider.TextBlock{Text: "405"},
			},
		}}
	}()
	return out, nil
}

// The behavior that makes adaptive models usable on the DEFAULT setting.
// Recording off exists to keep the model's chain-of-thought out of the session
// file; a withheld-thinking block contains no chain-of-thought to keep out, so
// dropping it would buy no privacy and cost the replay Anthropic expects.
func TestWithheldThinkingSurvivesRecordingOff(t *testing.T) {
	a := NewAgent(&adaptiveShapedClient{}, "fake-model", "system", Registry{})
	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	blocks := reasoningBlocksOf(t, a)
	var found *provider.ReasoningBlock
	for i := range blocks {
		if blocks[i].Shape == provider.ReasoningShapeAnthropicThinkingOpaque {
			found = &blocks[i]
		}
	}
	if found == nil {
		t.Fatal("recording-off dropped the withheld-thinking block; it has no readable text to withhold")
	}
	if found.Encrypted != "SIG" {
		t.Errorf("Encrypted = %q, want the signature — without it the block is unreplayable", found.Encrypted)
	}
	if found.Summary != "" {
		t.Errorf("Summary = %q, want empty", found.Summary)
	}
}

// The blast radius check. dropUnrecordableThinking must touch NOTHING else —
// a Codex block keeps its id and payload through a recording-off turn, which is
// the behavior every other provider already depends on.
func TestDropUnrecordableThinkingLeavesOtherProvidersAlone(t *testing.T) {
	in := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ReasoningBlock{ID: "rs_1", Summary: "codex", Encrypted: "OPAQUE"},
		provider.ReasoningBlock{Summary: "gemini thought"},
		provider.ReasoningBlock{Encrypted: "BLOB", Shape: provider.ReasoningShapeAnthropicRedacted},
		provider.ReasoningBlock{Encrypted: "SIG2", Shape: provider.ReasoningShapeAnthropicThinkingOpaque},
		provider.TextBlock{Text: "answer"},
		provider.ReasoningBlock{Summary: "anthropic", Encrypted: "SIG", Shape: provider.ReasoningShapeAnthropicThinking},
	}}

	got := dropUnrecordableThinking(in)
	if len(got.Content) != 5 {
		t.Fatalf("kept %d blocks, want 5: %#v", len(got.Content), got.Content)
	}
	for _, c := range got.Content {
		if rb, ok := c.(provider.ReasoningBlock); ok && rb.Shape == provider.ReasoningShapeAnthropicThinking {
			t.Errorf("the one block it exists to remove survived: %+v", rb)
		}
	}
	// Order is load-bearing: Anthropic reads reasoning position-sensitively,
	// so a filter that reshuffled the survivors would be a different bug.
	if tb, ok := got.Content[4].(provider.TextBlock); !ok || tb.Text != "answer" {
		t.Errorf("survivors were reordered: %#v", got.Content)
	}
}
