package provider

import (
	"strings"
	"testing"
)

// Promoting a reasoning-only turn (see openai_reasoning_replay_test.go) copies
// the model's own words into the visible content. When the reasoning still
// carries the thinking markers — because the server's parser never matched the
// closer and handed the whole reply back as reasoning — those markers ride
// along into the replayed history.
//
// The model then sees its previous answers apparently written with `</think>`
// in them and imitates the pattern, which makes the contamination
// self-reinforcing: every promoted turn teaches it to emit more markers.
//
// Observed against a local gemma4 build whose reply began mid-deliberation and
// closed with `</think>` before the real answer.
func TestOAIBuildRequest_PromotedReasoningDropsThinkMarkers(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	// The literal shape from the session that surfaced this: deliberation, the
	// closer, then the answer the user was meant to see.
	raw := "The user is asking what that note means.\nI'll suggest reading the docs.\n</think>That note is an injected context hint from your terva harness."
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "what is that note?"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: raw}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "thanks"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("the turn must still survive replay: got %d messages, want 3", len(out.Messages))
	}
	got, _ := out.Messages[1].Content.(string)
	if got == "" {
		t.Fatal("promoted content is empty: the turn lost its substance")
	}
	if containsAny(got, "</think>", "<think>") {
		t.Errorf("think markers replayed into content: %q", got)
	}
	// The text after the closer is what the model meant to say.
	if got != "That note is an injected context hint from your terva harness." {
		t.Errorf("promoted content = %q, want the answer that followed the closer", got)
	}
}

// The channel dialect gets the same treatment. A template that prefills
// `<|channel>thought` and never closes it strands the reply the same way.
func TestOAIBuildRequest_PromotedReasoningDropsChannelMarkers(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	raw := "<|channel>thought\nweighing the options\n<channel|>The visible answer."
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "go"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: raw}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := out.Messages[1].Content.(string)
	if containsAny(got, "<|channel>", "<channel|>") {
		t.Errorf("channel markers replayed into content: %q", got)
	}
	if got != "The visible answer." {
		t.Errorf("promoted content = %q, want the answer that followed the closer", got)
	}
}

// No closer at all: the parser swallowed everything and the reply is pure
// deliberation. There is no answer to recover, so the words are kept — losing
// the turn is what the promotion exists to prevent — but a stray opener must
// not ride along.
func TestOAIBuildRequest_PromotedReasoningWithoutCloserKeepsWords(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	raw := "<think>I never closed this block and just kept going."
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "go"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: raw}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("turn dropped: got %d messages, want 2", len(out.Messages))
	}
	got, _ := out.Messages[1].Content.(string)
	if containsAny(got, "<think>", "</think>") {
		t.Errorf("markers replayed into content: %q", got)
	}
	if got != "I never closed this block and just kept going." {
		t.Errorf("promoted content = %q, want the deliberation with the marker removed", got)
	}
}

// A turn whose reasoning is nothing but markers has no words to recover. It
// must not become an empty assistant message: Kimi rejects those outright
// ("assistant must not be empty"), which is the guard the promotion sits in
// front of.
func TestOAIBuildRequest_MarkerOnlyReasoningIsStillDropped(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "first"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: "<think>\n</think>"}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "second"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range out.Messages {
		if m.Role == "assistant" && m.Content == nil && len(m.ToolCalls) == 0 {
			t.Fatalf("message %d is an empty assistant: %+v", i, m)
		}
		if m.Role == "assistant" {
			if got, _ := m.Content.(string); got == "" {
				t.Fatalf("message %d has blank assistant content", i)
			}
		}
	}
	if got := len(out.Messages); got != 2 {
		t.Fatalf("messages=%d want 2: a marker-only turn has nothing to promote", got)
	}
}

// Reasoning that never carried a marker is promoted verbatim. The stripper
// must not chew on ordinary prose.
func TestOAIBuildRequest_PromotedReasoningWithoutMarkersUnchanged(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	raw := "Plain deliberation with no markers at all."
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "go"}}},
			{Role: RoleAssistant, Content: []Content{ReasoningBlock{Summary: raw}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := out.Messages[1].Content.(string)
	if got != raw {
		t.Errorf("promoted content = %q, want it unchanged", got)
	}
}

// Ordinary assistant text is never touched. Only the promotion path strips,
// so a message that legitimately discusses the markers in prose — this
// codebase does exactly that — keeps them.
func TestOAIBuildRequest_VisibleTextKeepsMarkersVerbatim(t *testing.T) {
	c := &openaiClient{name: "openai-compatible"}
	prose := "The marker pair is <think>…</think> for this family."
	out, err := c.buildRequest(Request{
		Model: "gemma-4-26b-a4b-it-qat",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "explain"}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: prose}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := out.Messages[1].Content.(string)
	if got != prose {
		t.Errorf("visible text = %q, want it verbatim: only promotion strips", got)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
