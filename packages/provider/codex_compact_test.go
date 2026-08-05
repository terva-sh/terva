package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The body captured verbatim from a live probe of the subscription backend,
// with the blob shortened. Fixtures written from the published docs would have
// encoded the wrong item type ("compaction" rather than "compaction_summary"),
// so this one is copied from what the endpoint actually answered.
const liveCompactResponse = `{
  "id": "resp_041f",
  "object": "response.compaction",
  "created_at": 1785882125,
  "output": [
    {"id":"msg_1","type":"message","status":"completed","role":"user",
     "content":[{"type":"input_text","text":"The capital of France is Paris. Remember that."}]},
    {"id":"msg_2","type":"message","status":"completed","role":"user",
     "content":[{"type":"input_text","text":"And the capital of Spain is Madrid."}]},
    {"id":"cmp_3","type":"compaction_summary","encrypted_content":"gAAAAABopaque=="}
  ],
  "usage": {
    "input_tokens": 63,
    "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0},
    "output_tokens": 75,
    "output_tokens_details": {"reasoning_tokens": 0},
    "total_tokens": 138
  }
}`

// The same endpoint, answering a REAL-SIZED request (26k of transcript rather
// than the 63-token toy above), captured live.
//
// It is here because the toy response omitted input_tokens_details entirely,
// and that omission was briefly read as "this endpoint does not report cache
// detail" — which would have made every cached_tokens reading meaningless.
// It does report it. At size it is explicitly present and explicitly zero,
// which is a fact about the endpoint rather than a gap in the parse, and the
// difference between those two matters enough to keep both fixtures.
const liveCompactResponseAtSize = `{
  "id": "resp_0c3303a668a26f73",
  "object": "response.compaction",
  "output": [
    {"id":"m1","type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
    {"id":"cmp_1","type":"compaction_summary","encrypted_content":"gAAAAABopaque=="}
  ],
  "usage": {
    "input_tokens": 26384,
    "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0},
    "output_tokens": 500,
    "output_tokens_details": {"reasoning_tokens": 0},
    "total_tokens": 26884
  }
}`

func compactTestClient(t *testing.T, h http.HandlerFunc) (*codexClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// The real base URL already ends in /responses, so the client appends
	// /compact to it. Mirror that here or the test would exercise a path
	// shape production never builds.
	return NewOpenAICodex("token", "acct", srv.URL+"/responses").(*codexClient), srv
}

func compactReq() Request {
	return Request{
		Model:  "gpt-5.6-terra",
		System: "you are a test",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "The capital of France is Paris. Remember that."}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "Noted."}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "And the capital of Spain is Madrid."}}},
		},
		PromptCacheKey: "sess-1",
	}
}

// The happy path, against the shape the backend really returns.
func TestCompactServerSideParsesTheLiveShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c, _ := compactTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, liveCompactResponse)
	})

	msgs, usage, err := c.CompactServerSide(context.Background(), compactReq())
	if err != nil {
		t.Fatal(err)
	}

	// The call is billed like any other, and an arm that reports its spend as
	// zero reads in an A/B against the client summarizers as FREE — the one
	// number that would make the comparison conclude the opposite of the truth.
	if usage.InputTokens != 63 || usage.OutputTokens != 75 {
		t.Errorf("usage = %+v; want the 63/75 the response reported", usage)
	}
	if usage.CostUSD <= 0 {
		t.Errorf("the compaction was priced at %v; ApplyCost never ran", usage.CostUSD)
	}

	if gotPath != "/responses/compact" {
		t.Errorf("posted to %q, want /responses/compact", gotPath)
	}
	// The endpoint takes no tools and does not stream; sending either would be
	// sending a shape it never asked for.
	for _, banned := range []string{"tools", "stream", "prompt_cache_options", "prompt_cache_retention"} {
		if _, ok := gotBody[banned]; ok {
			t.Errorf("compact body carries %q, which this endpoint does not take", banned)
		}
	}
	if gotBody["prompt_cache_key"] != "sess-1" {
		t.Errorf("compact body lost the cache key: %v", gotBody["prompt_cache_key"])
	}

	if len(msgs) != 3 {
		t.Fatalf("want 2 user turns + 1 compaction summary, got %d: %+v", len(msgs), msgs)
	}
	last, ok := msgs[2].Content[0].(CompactionBlock)
	if !ok {
		t.Fatalf("last item should be the compaction summary, got %T", msgs[2].Content[0])
	}
	if last.ID != "cmp_3" || last.Encrypted != "gAAAAABopaque==" {
		t.Errorf("compaction blob mangled: %+v", last)
	}
	if msgs[0].Role != RoleUser || msgs[1].Role != RoleUser {
		t.Errorf("the surviving turns should be the user's: %v, %v", msgs[0].Role, msgs[1].Role)
	}
}

// A result that cannot stand in for the conversation must be an ERROR, not a
// transcript.
//
// The caller replaces the conversation with whatever this returns, so every
// case here is a way of deleting history that would look like a successful
// compaction from the outside — smaller, well-formed, no error — and would only
// surface later, as a model answering from a conversation it has no memory of
// having had. The caller's fallback, summarizing client-side, is strictly
// better than any of them.
//
// The last case is the one that motivated the check and the one a reasonable
// implementation would miss: a reply carrying the user turns but no
// compaction_summary is not empty at all. It is a complete-looking transcript
// with the entire assistant half removed and nothing standing in for it.
func TestCompactServerSideRefusesAResultThatWouldDestroyHistory(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no output at all", `{"object":"response.compaction","output":[]}`},
		{"items we cannot use", `{"object":"response.compaction","output":[{"id":"x","type":"reasoning"}]}`},
		{"a summary with no blob", `{"object":"response.compaction","output":[{"id":"c","type":"compaction_summary","encrypted_content":""}]}`},
		{"user turns but no summary at all", `{"object":"response.compaction","output":[
			{"id":"m1","type":"message","role":"user","content":[{"type":"input_text","text":"remember this"}]},
			{"id":"m2","type":"message","role":"user","content":[{"type":"input_text","text":"and this"}]}
		]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := compactTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			})
			msgs, _, err := c.CompactServerSide(context.Background(), compactReq())
			if err == nil {
				t.Fatalf("want an error, got %d messages: %+v", len(msgs), msgs)
			}
			if len(msgs) != 0 {
				t.Errorf("an error must come with no messages, got %+v", msgs)
			}
		})
	}
}

// A non-200 surfaces as a ProviderError carrying the status, so the caller can
// tell "this backend has no such endpoint" from "the request was wrong" —
// which is the distinction the whole lead turned on.
func TestCompactServerSideSurfacesHTTPFailure(t *testing.T) {
	c, _ := compactTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"detail":"Unsupported parameter: nope"}`)
	})
	_, _, err := c.CompactServerSide(context.Background(), compactReq())
	if err == nil {
		t.Fatal("want an error for a 400")
	}
	if !strings.Contains(err.Error(), "400") && !strings.Contains(err.Error(), "Unsupported") {
		t.Errorf("error should carry the status or the body, got: %v", err)
	}
}

// The codex client must be reachable as a ServerCompactor THROUGH a wrapper.
// A bare type assertion on the outer client is the bug class ClientCaps exists
// to prevent, and a compactor probed the wrong way would silently report "this
// provider cannot compact server-side" for every wrapped client.
func TestServerCompactorFoundThroughAWrapper(t *testing.T) {
	inner := NewOpenAICodex("token", "acct", "https://example.test")
	if _, ok := ServerCompactorFor(inner); !ok {
		t.Fatal("codex client is not reported as a ServerCompactor")
	}

	// NewOpenAICodex returns the concrete client, so probing it proves nothing
	// about the wrapper walk. Wrap it the way the registry wraps
	// openai-responses and google-vertex: a bare type assertion on the outer
	// client fails HERE, silently reporting "cannot compact server-side" and
	// falling back forever with no error to notice.
	wrapped := &renamedClient{inner: inner, name: "openai-responses"}
	if _, ok := ServerCompactorFor(wrapped); !ok {
		t.Error("a wrapped codex client hides its compactor; the probe must walk Unwrap()")
	}

	if _, ok := ServerCompactorFor(NewOpenAI("token", "https://example.test")); ok {
		t.Error("the chat-completions client has no /compact endpoint and must not claim one")
	}
}

// The cached-token parse has to be exercised against a NON-ZERO value.
//
// Every live response measured so far reports cached_tokens: 0 — including one
// taken seconds after a dispatch of the same transcript that read 99% from
// cache, which is the observation behind "this endpoint does not use the prompt
// cache". But a fixture whose expected value is zero cannot tell a working
// parse from one that reads the wrong key and returns zero for everything.
// That is precisely how the endpoint's cache behaviour was nearly mis-reported
// once already, so the parse gets a case where zero is the WRONG answer.
func TestCompactUsageParsesCachedTokensWhenTheBackendReportsThem(t *testing.T) {
	body := `{"object":"response.compaction",
	  "output":[{"id":"c","type":"compaction_summary","encrypted_content":"blob"}],
	  "usage":{"input_tokens":26384,
	           "input_tokens_details":{"cached_tokens":20000,"cache_write_tokens":384},
	           "output_tokens":500}}`
	c, _ := compactTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})
	_, usage, err := c.CompactServerSide(context.Background(), compactReq())
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheReadTokens != 20000 {
		t.Errorf("CacheReadTokens = %d, want 20000 — the cached_tokens key is not being read",
			usage.CacheReadTokens)
	}
	if usage.CacheWriteTokens != 384 {
		t.Errorf("CacheWriteTokens = %d, want 384", usage.CacheWriteTokens)
	}
	// The three prompt fields are DISJOINT and priced separately, so the detail
	// comes OFF the total: 26384 - 20000 - 384.
	if usage.InputTokens != 6000 {
		t.Errorf("InputTokens = %d, want 6000 (26384 - 20000 cached - 384 written) — "+
			"leaving the total intact double-counts the cached tokens at full price",
			usage.InputTokens)
	}
}

// And the real-sized live shape parses to what it says: everything full price.
func TestCompactUsageAtRealSizeReportsNoCacheParticipation(t *testing.T) {
	c, _ := compactTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, liveCompactResponseAtSize)
	})
	_, usage, err := c.CompactServerSide(context.Background(), compactReq())
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheReadTokens != 0 || usage.InputTokens != 26384 {
		t.Errorf("usage = %+v; the measured live shape is 26,384 input with zero cached", usage)
	}
}
