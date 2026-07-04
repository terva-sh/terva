package core

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// ephemeralCaptureClient records the EphemeralContext of every request it sees.
type ephemeralCaptureClient struct {
	mu        sync.Mutex
	ephemeral []string
}

func (c *ephemeralCaptureClient) Name() string { return "capture" }

func (c *ephemeralCaptureClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	c.mu.Unlock()
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "capture", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// ContextProvider output must reach the provider request but never the
// persisted transcript.
func TestContextProviderInjectsEphemeralNotTranscript(t *testing.T) {
	client := &ephemeralCaptureClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	const marker = "<extension-context source=\"terva-tasks\">active: patch parser</extension-context>"
	a.ContextProvider = func() string { return marker }

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.ephemeral) == 0 {
		t.Fatal("no request captured")
	}
	if client.ephemeral[0] != marker {
		t.Errorf("request EphemeralContext = %q, want the marker", client.ephemeral[0])
	}

	// The marker must not appear anywhere in the durable transcript.
	for _, m := range a.Messages() {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, "extension-context") {
				t.Errorf("ephemeral context leaked into transcript: %q", tb.Text)
			}
		}
	}
}

// stopEndCountingClient always finishes (StopEnd) and counts calls, so
// a test can see how many turns the at-close gate drove.
type stopEndCountingClient struct{ calls int }

func (c *stopEndCountingClient) Name() string { return "stopend" }
func (c *stopEndCountingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.calls++
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "stopend", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// The at-close gate re-prompts once when ContinueOnStop says to, appends
// the nudge as a user turn, and is capped to a single re-prompt even
// when the host always says "continue".
func TestAtCloseGateFiresOnceThenStops(t *testing.T) {
	c := &stopEndCountingClient{}
	a := NewAgent(c, "fake-model", "system", Registry{})
	a.ContinueOnStop = func(stop provider.StopReason) (bool, string) {
		return true, "REVIEW-OPEN-WORK" // always asks to continue
	}
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if c.calls != 2 {
		t.Errorf("want 2 model calls (initial + one gate re-prompt), got %d", c.calls)
	}
	found := false
	for _, m := range a.Messages() {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, ct := range m.Content {
			if tb, ok := ct.(provider.TextBlock); ok && strings.Contains(tb.Text, "REVIEW-OPEN-WORK") {
				found = true
			}
		}
	}
	if !found {
		t.Error("gate nudge was not appended as a user message")
	}
}

// When ContinueOnStop declines, the run ends with no extra turn.
func TestAtCloseGateDeclineEndsRun(t *testing.T) {
	c := &stopEndCountingClient{}
	a := NewAgent(c, "fake-model", "system", Registry{})
	a.ContinueOnStop = func(stop provider.StopReason) (bool, string) { return false, "" }
	if err := a.Prompt(context.Background(), "hi", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if c.calls != 1 {
		t.Errorf("decline should not add a turn; want 1 call, got %d", c.calls)
	}
}

// With no ContextProvider, EphemeralContext stays empty.
func TestNoContextProviderMeansEmptyEphemeral(t *testing.T) {
	client := &ephemeralCaptureClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.ephemeral) == 0 || client.ephemeral[0] != "" {
		t.Errorf("expected empty EphemeralContext, got %q", client.ephemeral)
	}
}

// TestSetContextProviderLive covers the live setter used to re-wire lore after an
// edit: SetContextProvider swaps the per-turn provider, and the next read sees it.
func TestSetContextProviderLive(t *testing.T) {
	a := NewAgent(nil, "m", "", Registry{})
	if a.ContextProvider != nil {
		t.Fatal("fresh agent should have no context provider")
	}
	a.SetContextProvider(func() string { return "LORE_MARKER" })
	if a.ContextProvider == nil || a.ContextProvider() != "LORE_MARKER" {
		t.Fatal("provider not set")
	}
	a.SetContextProvider(func() string { return "UPDATED" })
	if a.ContextProvider() != "UPDATED" {
		t.Errorf("provider not swapped: got %q", a.ContextProvider())
	}
}
