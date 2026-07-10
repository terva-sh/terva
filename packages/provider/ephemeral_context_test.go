package provider

import (
	"strings"
	"testing"
)

// Anthropic: the ephemeral block must be a trailing user message AFTER
// the cache breakpoint — i.e. the prior last user message keeps the
// cache_control marker, and the appended block carries none. That's
// what preserves the cached prefix while the live block re-processes.
func TestAnthropicEphemeralAfterCacheBreakpoint(t *testing.T) {
	c := NewAnthropic("token", "https://example.test").(*anthropicClient)
	req, err := c.buildRequest(Request{
		Model: "claude-haiku-4-5",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "do the thing"}}},
		},
		EphemeralContext: "<extension-context source=\"terva-tasks\">active foo</extension-context>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("want 2 messages (real + ephemeral), got %d", len(req.Messages))
	}
	// Last message is the ephemeral block, carrying the text, uncached.
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		t.Errorf("ephemeral message role = %q, want user", last.Role)
	}
	tb, ok := last.Content[0].(anthTextBlock)
	if !ok || !strings.Contains(tb.Text, "active foo") {
		t.Fatalf("ephemeral block missing text: %+v", last.Content)
	}
	if tb.CacheControl != nil {
		t.Error("ephemeral block must NOT carry cache_control (it would waste breakpoint #4)")
	}
	// The breakpoint stays on the real prior message.
	prev := req.Messages[0]
	ptb, ok := prev.Content[len(prev.Content)-1].(anthTextBlock)
	if !ok || ptb.CacheControl == nil {
		t.Errorf("cache breakpoint should remain on the last real user message, got %+v", prev.Content)
	}
}

// With no ephemeral context, nothing is appended.
func TestAnthropicNoEphemeralWhenEmpty(t *testing.T) {
	c := NewAnthropic("token", "https://example.test").(*anthropicClient)
	req, err := c.buildRequest(Request{
		Model:    "claude-haiku-4-5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(req.Messages))
	}
}

// OpenAI Responses (codex): the ephemeral block is appended as a trailing
// user input message. The builder used to drop EphemeralContext entirely,
// so on openai-codex / openai-responses the model never saw extension
// context cards or the context-pressure note.
func TestOpenAICodexEphemeralTrailingMessage(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model:            "gpt-5.5",
		Messages:         []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		EphemeralContext: "<extension-context source=\"x\">live</extension-context>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Input) != 2 {
		t.Fatalf("want 2 input items (real + ephemeral), got %d", len(wire.Input))
	}
	msg, ok := wire.Input[len(wire.Input)-1].(codexInputMessage)
	if !ok || msg.Role != "user" {
		t.Fatalf("ephemeral item not a trailing user input message: %T %+v", wire.Input[len(wire.Input)-1], wire.Input[len(wire.Input)-1])
	}
	txt, ok := msg.Content[0].(codexInputText)
	if !ok || !strings.Contains(txt.Text, "live") {
		t.Fatalf("ephemeral block missing text: %+v", msg.Content)
	}
}

// With no ephemeral context, the codex builder appends nothing.
func TestOpenAICodexNoEphemeralWhenEmpty(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model:    "gpt-5.5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Input) != 1 {
		t.Fatalf("want 1 input item, got %d", len(wire.Input))
	}
}

// OpenAI: the ephemeral block is appended as a trailing user message.
func TestOpenAIEphemeralTrailingMessage(t *testing.T) {
	c := NewOpenAI("token", "https://example.test").(*openaiClient)
	req, err := c.buildRequest(Request{
		Model:            "m",
		Messages:         []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		EphemeralContext: "<extension-context source=\"x\">live</extension-context>",
	})
	if err != nil {
		t.Fatal(err)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		t.Errorf("ephemeral role = %q, want user", last.Role)
	}
	s, ok := last.Content.(string)
	if !ok || !strings.Contains(s, "live") {
		t.Fatalf("ephemeral content not a string with the text: %T %v", last.Content, last.Content)
	}
}
