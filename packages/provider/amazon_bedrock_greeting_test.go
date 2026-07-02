package provider

import "testing"

// A --card seeds its opening greeting as a leading assistant turn. Bedrock
// Converse rejects a conversation that does not begin with a user turn, so
// buildRequest must prepend a request-scoped user turn (EnsureLeadingUserTurn).
func TestBedrockBuildRequest_LeadingAssistantGetsUserGuard(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Messages: []Message{
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Hello, traveler."}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) < 3 {
		t.Fatalf("expected a prepended user turn (3 messages), got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("first role = %q, want user (guard)", req.Messages[0].Role)
	}
	if req.Messages[1].Role != "assistant" {
		t.Errorf("second role = %q, want assistant (greeting)", req.Messages[1].Role)
	}
}

// A normal user-first conversation is untouched by the guard.
func TestBedrockBuildRequest_NormalConversationUnchanged(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Errorf("normal conversation changed: %d msgs, first=%q", len(req.Messages), req.Messages[0].Role)
	}
}
