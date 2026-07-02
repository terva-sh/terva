package provider

import "testing"

// A --card seeds its opening greeting as a leading assistant turn. Gemini
// rejects a leading role:"model" content, so buildRequest must prepend a
// request-scoped user turn (EnsureLeadingUserTurn).
func TestGeminiBuildRequest_LeadingAssistantGetsUserGuard(t *testing.T) {
	c := &geminiClient{}
	out, _, err := c.buildRequest(Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Hello, traveler."}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contents) < 3 {
		t.Fatalf("expected a prepended user turn (3 contents), got %d", len(out.Contents))
	}
	if out.Contents[0].Role != "user" {
		t.Errorf("first role = %q, want user (guard)", out.Contents[0].Role)
	}
	if out.Contents[1].Role != "model" {
		t.Errorf("second role = %q, want model (greeting)", out.Contents[1].Role)
	}
}

// A normal user-first conversation is untouched by the guard.
func TestGeminiBuildRequest_NormalConversationUnchanged(t *testing.T) {
	c := &geminiClient{}
	out, _, err := c.buildRequest(Request{
		Model:    "gemini-2.5-flash",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contents) != 1 || out.Contents[0].Role != "user" {
		t.Errorf("normal conversation changed: %d contents, first=%q", len(out.Contents), out.Contents[0].Role)
	}
}
