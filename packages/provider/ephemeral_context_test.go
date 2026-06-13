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
