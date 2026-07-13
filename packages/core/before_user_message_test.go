package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// endTurnClient finishes every turn immediately with a short reply, so a
// Prompt() runs the loop once and returns. Enough to exercise the
// BeforeUserMessage gate, which runs before any model call anyway.
type endTurnClient struct{}

func (endTurnClient) Name() string { return "end-turn-fake" }
func (endTurnClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{}
}
func (endTurnClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "end-turn-fake", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// userTexts pulls the text of every user-role message in a transcript.
func userTexts(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				out = append(out, tb.Text)
			}
		}
	}
	return out
}

// A BeforeUserMessage refusal of the initial prompt records nothing in
// the transcript, fires EvUserMessageRejected (not EvUserMessage), ends
// the run with EvDone, and never reaches the model.
func TestBeforeUserMessageBlocksInitialPrompt(t *testing.T) {
	a := NewAgent(endTurnClient{}, "test", "", Registry{})
	a.BeforeUserMessage = func(string) (bool, string, string) {
		return false, "denied", ""
	}

	var (
		rejectedReason string
		gotRejected    bool
		sawDone        bool
		sawUser        bool
		sawAssistant   bool
	)
	err := a.Prompt(context.Background(), "secret prompt", nil, func(ev AgentEvent) {
		switch e := ev.(type) {
		case EvUserMessageRejected:
			gotRejected = true
			rejectedReason = e.Reason
		case EvDone:
			sawDone = true
		case EvUserMessage:
			sawUser = true
		case EvAssistantMessage:
			sawAssistant = true
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if !gotRejected {
		t.Fatal("expected EvUserMessageRejected")
	}
	if rejectedReason != "denied" {
		t.Errorf("reason = %q, want %q", rejectedReason, "denied")
	}
	if !sawDone {
		t.Error("expected EvDone after a blocked prompt")
	}
	if sawUser {
		t.Error("EvUserMessage must not fire for a blocked prompt")
	}
	if sawAssistant {
		t.Error("no turn should run for a blocked prompt")
	}
	if texts := userTexts(a.Messages()); len(texts) != 0 {
		t.Errorf("blocked prompt left transcript entries: %v", texts)
	}
}

// A non-empty replacement rewrites the prompt the model sees — and the
// rewrite is what lands in the transcript and rides the EvUserMessage.
func TestBeforeUserMessageRewritesInitialPrompt(t *testing.T) {
	a := NewAgent(endTurnClient{}, "test", "", Registry{})
	a.BeforeUserMessage = func(string) (bool, string, string) {
		return true, "", "REWRITTEN"
	}

	var evText string
	err := a.Prompt(context.Background(), "original", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessage); ok {
			evText = extractText(e.Message)
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	texts := userTexts(a.Messages())
	if len(texts) != 1 || texts[0] != "REWRITTEN" {
		t.Errorf("transcript user texts = %v, want [REWRITTEN]", texts)
	}
	if evText != "REWRITTEN" {
		t.Errorf("EvUserMessage text = %q, want REWRITTEN", evText)
	}
}

// A prompt queued while the agent is busy is gated by the same guard, so
// a guard can't be bypassed by typing mid-turn. The bad queued message
// is dropped from the transcript while the good initial prompt remains.
func TestBeforeUserMessageGatesQueuedDrain(t *testing.T) {
	a := NewAgent(endTurnClient{}, "test", "", Registry{})
	a.BeforeUserMessage = func(text string) (bool, string, string) {
		if text == "queued-bad" {
			return false, "no", ""
		}
		return true, "", ""
	}
	if !a.QueueMessage("queued-bad") {
		t.Fatal("QueueMessage refused a non-empty message")
	}

	var rejected []string
	err := a.Prompt(context.Background(), "initial-ok", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessageRejected); ok {
			rejected = append(rejected, e.Text)
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	var foundInitial, foundQueued bool
	for _, tx := range userTexts(a.Messages()) {
		switch tx {
		case "initial-ok":
			foundInitial = true
		case "queued-bad":
			foundQueued = true
		}
	}
	if !foundInitial {
		t.Error("initial-ok missing from transcript")
	}
	if foundQueued {
		t.Error("queued-bad should have been gated out of the drain")
	}
	if len(rejected) != 1 || rejected[0] != "queued-bad" {
		t.Errorf("rejected = %v, want [queued-bad]", rejected)
	}
}

// The synthetic at-close gate nudge (a continuation gate's re-prompt) is the
// host's words, not the user's — it must never reach the BeforeUserMessage guard.
func TestBeforeUserMessageSkipsSyntheticGateNudge(t *testing.T) {
	a := NewAgent(endTurnClient{}, "test", "", Registry{})

	var seen []string
	a.BeforeUserMessage = func(text string) (bool, string, string) {
		seen = append(seen, text)
		return true, "", ""
	}
	// Fire the at-close nudge exactly once (the default per-Prompt cap), so
	// the loop runs a second step that drains the synthetic nudge.
	a.AddContinuationGate(ContinuationGate{Cause: "test", Fire: func(provider.StopReason) (string, bool) {
		return "OPEN-WORK-NUDGE", true
	}})

	var userTextsSeen []string
	err := a.Prompt(context.Background(), "hello", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessage); ok {
			userTextsSeen = append(userTextsSeen, extractText(e.Message))
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	for _, s := range seen {
		if s == "OPEN-WORK-NUDGE" {
			t.Error("the synthetic gate nudge must not be passed to BeforeUserMessage")
		}
	}
	if len(seen) != 1 || seen[0] != "hello" {
		t.Errorf("guard saw %v, want [hello]", seen)
	}
	// The nudge still enters the transcript as a (synthetic) user message;
	// it just isn't gated.
	var sawNudge bool
	for _, tx := range userTextsSeen {
		if tx == "OPEN-WORK-NUDGE" {
			sawNudge = true
		}
	}
	if !sawNudge {
		t.Error("the synthetic nudge should still be appended as a user message")
	}
}
