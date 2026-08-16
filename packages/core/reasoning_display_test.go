package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Showing the model's thinking and RECORDING it are separate decisions, and the
// separation only means anything if display leaves the transcript alone.

// reasoningFakeClient streams a reasoning delta and finishes with a message
// carrying a fully-populated ReasoningBlock — summary, id, and opaque payload,
// the shape the openai backends actually return.
type reasoningFakeClient struct {
	lastReq provider.Request
}

func (c *reasoningFakeClient) Name() string { return "reasoning-fake" }

func (c *reasoningFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.lastReq = req
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "reasoning-fake", Model: req.Model}
		out <- provider.EventReasoningDelta{Delta: "**Reading the config**"}
		out <- provider.EventTextDelta{Delta: "done"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.TextBlock{Text: "done"},
				provider.ReasoningBlock{
					ID:        "rs_1",
					Summary:   "**Reading the config**",
					Encrypted: "OPAQUE",
				},
			},
		}}
	}()
	return out, nil
}

// reasoningBlockOf returns the trailing assistant message's ReasoningBlock.
func reasoningBlockOf(t *testing.T, a *Agent) provider.ReasoningBlock {
	t.Helper()
	msgs := a.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	for _, c := range msgs[len(msgs)-1].Content {
		if rb, ok := c.(provider.ReasoningBlock); ok {
			return rb
		}
	}
	t.Fatal("no ReasoningBlock on the assistant message")
	return provider.ReasoningBlock{}
}

// Display-only: the summary reaches the SINK and is blanked in the TRANSCRIPT.
//
// 🪤 The block itself must survive with its id and opaque payload intact. Those
// have to round-trip on the next request exactly like a gemini thought
// signature, so hiding the text by dropping the block would strand the
// provider's reasoning item and break the following turn.
func TestShowReasoningKeepsTheSummaryOffTheTranscript(t *testing.T) {
	client := &reasoningFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.SetShowReasoning(true) // display on, persistence off

	var live string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if d, ok := ev.(EvReasoningDelta); ok {
			live += d.Delta
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if live != "**Reading the config**" {
		t.Errorf("live reasoning reaching the UI = %q, want the summary", live)
	}
	rb := reasoningBlockOf(t, a)
	if rb.Summary != "" {
		t.Errorf("summary was persisted with display-only on: %q", rb.Summary)
	}
	if rb.ID != "rs_1" || rb.Encrypted != "OPAQUE" {
		t.Errorf("blanking the summary damaged the block: %+v", rb)
	}
	// The request had to ASK, or no provider would have sent a summary at all.
	if got := client.lastReq.ReasoningSummary; got != "auto" {
		t.Errorf("request ReasoningSummary = %q, want %q", got, "auto")
	}
}

// The other half: with persistence chosen, the summary STAYS. Without this a
// blanket "always blank it" would pass the test above and silently delete the
// thing reasoning_summary exists to keep.
func TestRecordReasoningKeepsTheSummaryInTheTranscript(t *testing.T) {
	client := &reasoningFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.SetReasoningSummary("detailed")
	a.SetShowReasoning(true) // both on

	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if rb := reasoningBlockOf(t, a); rb.Summary != "**Reading the config**" {
		t.Errorf("summary lost with recording on: %q", rb.Summary)
	}
	// Persist wins the detail level: the display toggle must not quietly
	// downgrade an operator's explicit "detailed" to "auto".
	if got := client.lastReq.ReasoningSummary; got != "detailed" {
		t.Errorf("request ReasoningSummary = %q, want %q", got, "detailed")
	}
}

// With neither switch on, nothing is requested — the byte-identical baseline
// the split promises for anyone who never touches either setting.
func TestNeitherReasoningSwitchAsksForASummary(t *testing.T) {
	client := &reasoningFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})

	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := client.lastReq.ReasoningSummary; got != "" {
		t.Errorf("request asked for a summary with both switches off: %q", got)
	}
}

func TestReasoningSummaryRequestPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		persist string
		show    bool
		want    string
	}{
		{"neither", "", false, ""},
		{"display only asks auto", "", true, "auto"},
		{"persist alone", "concise", false, "concise"},
		{"persist wins over display", "detailed", true, "detailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasoningSummaryRequest(tc.persist, tc.show); got != tc.want {
				t.Errorf("reasoningSummaryRequest(%q, %v) = %q, want %q", tc.persist, tc.show, got, tc.want)
			}
		})
	}
}
