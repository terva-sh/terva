package provider

import "testing"

// A leading assistant message (a character card's seeded greeting) must be
// made valid for Anthropic by prepending a request-scoped user turn.
func TestBuildRequest_LeadingAssistantGetsUserGuard(t *testing.T) {
	c := &anthropicClient{}
	out, err := c.buildRequest(Request{
		Model: "claude-sonnet-4-5",
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
func TestBuildRequest_NormalConversationUnchanged(t *testing.T) {
	c := &anthropicClient{}
	out, err := c.buildRequest(Request{
		Model:    "claude-sonnet-4-5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Errorf("normal conversation changed: %d msgs, first=%q", len(out.Messages), out.Messages[0].Role)
	}
}
