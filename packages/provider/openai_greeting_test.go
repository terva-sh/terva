package provider

import "testing"

// A leading assistant message (a character card's seeded greeting) gets the
// request-scoped user-turn guard on the OpenAI-family wire too: OpenAI proper
// tolerates assistant-first, but this builder serves every OpenAI-compatible
// clone via newOpenAICompat (Moonshot/Kimi, strict local templates), where
// user-first can be enforced. Mirrors the anthropic/bedrock/gemini tests.
func TestOAIBuildRequest_LeadingAssistantGetsUserGuard(t *testing.T) {
	c := &openaiClient{name: "openai"}
	out, err := c.buildRequest(Request{
		Model: "gpt-5",
		Messages: []Message{
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Hello, traveler."}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) < 3 {
		t.Fatalf("expected a prepended user turn (3 messages), got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "user" {
		t.Errorf("first role = %q, want user (guard)", out.Messages[0].Role)
	}
	if out.Messages[1].Role != "assistant" {
		t.Errorf("second role = %q, want assistant (greeting)", out.Messages[1].Role)
	}
}

// A normal user-first conversation is untouched by the guard.
func TestOAIBuildRequest_NormalConversationUnchanged(t *testing.T) {
	c := &openaiClient{name: "openai"}
	out, err := c.buildRequest(Request{
		Model:    "gpt-5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Errorf("normal conversation changed: %d msgs, first=%q", len(out.Messages), out.Messages[0].Role)
	}
}

// The Codex/Responses builder applies the same guard to its input items.
func TestCodexBuildRequest_LeadingAssistantGetsUserGuard(t *testing.T) {
	c := &codexClient{}
	out, err := c.buildRequest(Request{
		Model: "gpt-5",
		Messages: []Message{
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Hello, traveler."}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Input) < 3 {
		t.Fatalf("expected guard + greeting + user turn in input, got %d items", len(out.Input))
	}
	first, ok := out.Input[0].(codexInputMessage)
	if !ok || first.Role != "user" {
		t.Errorf("first input item should be the user guard, got %#v", out.Input[0])
	}
}
