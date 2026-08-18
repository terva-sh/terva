package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Usage.InputTokens is documented as the UNCACHED remainder of the prompt,
// never the whole prompt: "Anthropic reports it that way natively; the OpenAI,
// Codex and Gemini decoders subtract their cached count to match."
//
// Four decoders hand-roll that subtraction. Two were pinned by tests; these are
// the other two. The consumers named in that doc — the context gauge, the
// compaction thresholds, CacheHitRate — all read the prompt as
// input + cache_read + cache_write, so a decoder that stops subtracting
// double-counts every cached token. Nothing else would notice: the numbers stay
// plausible, they are just wrong, and this review has already found one live
// instance of that shape (the image-token clamp, ~35% under-billing).

// openaiChatUsage drives one chat-completions SSE response and returns the usage
// it reported.
func openaiChatUsage(t *testing.T, frame string) Usage {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + frame + "\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	evs, err := NewOpenAI("k", srv.URL).Stream(context.Background(), Request{
		Model:    "gpt-5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Usage
	for ev := range evs {
		if e, ok := ev.(EventUsage); ok {
			got = e.Usage
		}
	}
	return got
}

// TestOpenAIChatReportsTheUncachedRemainder: prompt_tokens is the WHOLE prompt
// and cached_tokens is a subset of it, so the remainder is the difference.
func TestOpenAIChatReportsTheUncachedRemainder(t *testing.T) {
	got := openaiChatUsage(t, `{"choices":[{"delta":{"content":"hi"}}],`+
		`"usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":900}}}`)

	if got.CacheReadTokens != 900 {
		t.Fatalf("CacheReadTokens = %d, want 900 — the fixture is not reaching the decoder", got.CacheReadTokens)
	}
	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (1000 prompt − 900 cached). Left as the whole prompt, "+
			"every consumer reading input+cache_read counts the cached 900 twice", got.InputTokens)
	}
	// The property the subtraction exists to preserve, stated as the consumers
	// state it: the three prompt fields are DISJOINT and sum to the prompt.
	if sum := got.InputTokens + got.CacheReadTokens + got.CacheWriteTokens; sum != 1000 {
		t.Errorf("input+cache_read+cache_write = %d, want the 1000 the API reported", sum)
	}
}

// TestOpenAIChatKeepsTheWholePromptWhenCachedExceedsIt: a provider reporting
// cached tokens ABOVE the prompt total would drive the remainder negative, which
// is not a number any consumer can use. The decoder degrades to the whole
// prompt instead — a documented fallback, and the only branch of the
// subtraction that is not the happy path.
func TestOpenAIChatKeepsTheWholePromptWhenCachedExceedsIt(t *testing.T) {
	got := openaiChatUsage(t, `{"choices":[{"delta":{"content":"hi"}}],`+
		`"usage":{"prompt_tokens":50,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":900}}}`)

	if got.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want the whole prompt (50) rather than a negative remainder", got.InputTokens)
	}
}

// TestGeminiReportsTheUncachedRemainder: the same contract, third copy.
// promptTokenCount is the whole prompt; cachedContentTokenCount is a subset.
func TestGeminiReportsTheUncachedRemainder(t *testing.T) {
	got := geminiUsage(t, "gemini-3.1-pro",
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20,"cachedContentTokenCount":900}}`)

	if got.CacheReadTokens != 900 {
		t.Fatalf("CacheReadTokens = %d, want 900 — the fixture is not reaching the decoder", got.CacheReadTokens)
	}
	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (1000 prompt − 900 cached)", got.InputTokens)
	}
	if sum := got.InputTokens + got.CacheReadTokens + got.CacheWriteTokens; sum != 1000 {
		t.Errorf("input+cache_read+cache_write = %d, want the 1000 the API reported", sum)
	}
}
