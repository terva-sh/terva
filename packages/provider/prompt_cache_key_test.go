package provider

import "testing"

// OpenAI Responses (codex): the per-conversation cache key is forwarded so
// concurrent sessions on one account (a coordinator plus swarm children)
// stop evicting each other's cached prefixes.
func TestOpenAICodexPromptCacheKey(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model:          "gpt-5.5",
		Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		PromptCacheKey: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.PromptCacheKey != "sess-1" {
		t.Errorf("codex prompt_cache_key = %q, want sess-1", wire.PromptCacheKey)
	}
}

// Chat Completions: the key is forwarded on the real OpenAI backend only —
// the same client serves OpenAI-compatible endpoints (kimi, ollama, azure,
// copilot) that may reject unknown parameters.
func TestOpenAIPromptCacheKeyGatedByBackend(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}

	real := NewOpenAI("token", "https://example.test").(*openaiClient)
	req, err := real.buildRequest(Request{Model: "m", Messages: msgs, PromptCacheKey: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if req.PromptCacheKey != "sess-1" {
		t.Errorf("openai prompt_cache_key = %q, want sess-1", req.PromptCacheKey)
	}

	kimi := NewKimi("token", "https://example.test").(*openaiClient)
	kreq, err := kimi.buildRequest(Request{Model: "m", Messages: msgs, PromptCacheKey: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if kreq.PromptCacheKey != "" {
		t.Errorf("kimi must not receive prompt_cache_key, got %q", kreq.PromptCacheKey)
	}
}
