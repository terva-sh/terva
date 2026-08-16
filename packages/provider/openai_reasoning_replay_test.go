package provider

import "testing"

// A local OpenAI-compatible server (LM Studio's llama.cpp runtime, vLLM with a
// reasoning parser) can return a whole answer in `reasoning_content` with
// `content` left empty: a thinking channel that never closes leaves every
// token after the opener classified as reasoning. The marker pair varies by
// model (`<think>…</think>`, `<|channel>thought…<channel|>`), and a chat
// template can force it by prefilling an opener it never closes. The stream
// assembles that turn as a lone ReasoningBlock.
//
// The empty-assistant guard exists because Kimi rejects an assistant message
// with neither text nor tool calls, but it used to drop such a turn from the
// replay entirely. The model then saw a gap where its own answer had been and
// apologised for a "technical snag" it had no record of. The reasoning is the
// turn's only surviving substance, so it rides back as the content.
func TestOAIBuildRequest_ReasoningOnlyTurnSurvivesReplay(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "how is this one?"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: "the answer the user never saw", Shape: ReasoningShapeOpenAIChat}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "thoughts on that?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("reasoning-only assistant turn dropped from replay: got %d messages, want 3", len(out.Messages))
	}
	if out.Messages[1].Role != "assistant" {
		t.Fatalf("second message role = %q, want assistant", out.Messages[1].Role)
	}
	got, ok := out.Messages[1].Content.(string)
	if !ok {
		t.Fatalf("assistant content = %T, want string", out.Messages[1].Content)
	}
	if got != "the answer the user never saw" {
		t.Errorf("assistant content = %q, want the reasoning text", got)
	}
}

// The Kimi guard itself stays intact: an assistant turn carrying nothing at
// all has no substance to replay and must still be dropped, or the endpoint
// 400s with "assistant must not be empty".
func TestOAIBuildRequest_TrulyEmptyAssistantTurnStillDropped(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "   "}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "still there?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Messages {
		if m.Role == "assistant" {
			t.Fatalf("empty assistant turn survived replay: %+v", m)
		}
	}
}

// A reasoning-only turn is promoted to content, not smuggled back in
// reasoning_content: that field is only valid alongside a tool call on the
// endpoints that read it, and Kimi 400s on an assistant message whose only
// substance is reasoning_content.
func TestOAIBuildRequest_PromotedReasoningIsNotAlsoReasoningContent(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "go"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: "promoted", Shape: ReasoningShapeOpenAIChat}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) < 2 {
		t.Fatalf("reasoning-only assistant turn dropped from replay: got %d messages, want 2", len(out.Messages))
	}
	if out.Messages[1].ReasoningContent != "" {
		t.Errorf("reasoning_content = %q, want empty on a promoted reasoning-only turn", out.Messages[1].ReasoningContent)
	}
}

// The established Kimi path is untouched: reasoning alongside a tool call
// still rides in reasoning_content, and the visible text stays the content.
func TestOAIBuildRequest_ReasoningWithToolCallUnchanged(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "search it"}}},
			{Role: RoleAssistant, Content: []Content{
				TextBlock{Text: "Let me look."},
				ToolCallBlock{ID: "call_1", Name: "web_search", Arguments: []byte(`{"q":"x"}`)},
				ReasoningBlock{Summary: "deliberation", Shape: ReasoningShapeOpenAIChat},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	am := out.Messages[1]
	if am.ReasoningContent != "deliberation" {
		t.Errorf("reasoning_content = %q, want the deliberation", am.ReasoningContent)
	}
	if got, _ := am.Content.(string); got != "Let me look." {
		t.Errorf("content = %q, want the visible text", got)
	}
	if len(am.ToolCalls) != 1 {
		t.Errorf("tool calls = %d, want 1", len(am.ToolCalls))
	}
}
