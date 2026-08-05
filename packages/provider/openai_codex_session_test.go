package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// codexCaptureServer stands in for the Codex endpoint and hands back the
// headers of the request it received. Uses httptest like the rest of this
// package's provider tests rather than a bespoke RoundTripper.
func codexCaptureServer(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header { return got }
}

// The session-id header is worth having only if it is STABLE across a
// conversation's dispatches. A fresh id per request reproduces the hit rate of
// sending no header at all while looking, in a diff and in a header dump,
// exactly like the fix was applied — so stability is the property under test
// here, not merely the presence of a header.
func TestCodexSessionIDIsStableForOneConversation(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	first := codexSessionID(key)
	if first == "" {
		t.Fatal("codexSessionID returned empty for a non-empty cache key")
	}
	for i := 0; i < 8; i++ {
		if got := codexSessionID(key); got != first {
			t.Fatalf("codexSessionID is not stable: call %d gave %q, first gave %q", i+2, got, first)
		}
	}
}

func TestCodexSessionIDIsDistinctPerConversation(t *testing.T) {
	seen := map[string]string{}
	for _, key := range []string{
		"0e3c4d40-b787-8bcc-9f12-20260804154657",
		"20260804-195015-dd0aff9f", // legacy transcripts key on a file basename
		"live-9a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"session.json", // every swarm child's transcript is named this
	} {
		got := codexSessionID(key)
		if got == "" {
			t.Fatalf("codexSessionID(%q) = empty", key)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("codexSessionID collided: %q and %q both gave %q", prev, key, got)
		}
		seen[got] = key
	}
}

// cacheID is not always UUID-shaped — legacy transcripts key on a file
// basename and live-only agents on "live-<uuid>" — while the Codex CLI sends a
// UUID. The derivation exists to normalize every shape to one the backend
// certainly accepts, so a non-UUID input must still yield a valid UUID.
func TestCodexSessionIDIsAlwaysAUUID(t *testing.T) {
	for _, key := range []string{
		"20260804-195015-dd0aff9f",
		"session.json",
		"live-9a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		strings.Repeat("x", 300),
	} {
		got := codexSessionID(key)
		if _, err := uuid.Parse(got); err != nil {
			t.Fatalf("codexSessionID(%q) = %q, which is not a UUID: %v", key, got, err)
		}
	}
}

// With no cache key there is no stable conversation identity to derive from,
// and a random id would be strictly WORSE than none: it would measure like the
// baseline while presenting as the fix. Sending nothing is the honest answer.
func TestCodexSessionIDIsEmptyWithoutACacheKey(t *testing.T) {
	if got := codexSessionID(""); got != "" {
		t.Fatalf("codexSessionID(\"\") = %q, want empty", got)
	}
}

// The wire test: the header actually goes out, carries the derived value, and
// — the half that keeps this from becoming an impersonation change — terva
// still names ITSELF in originator and user-agent. The decomposition measured
// those two contributing nothing, so changing them would buy no cache and cost
// the honest self-identification.
func TestCodexStreamSendsSessionIDWithoutSpoofingIdentity(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	srv, headers := codexCaptureServer(t)
	c := NewOpenAICodex("token", "acct", srv.URL)

	if _, err := c.Stream(context.Background(), Request{
		Model:          "gpt-5.6-sol",
		PromptCacheKey: key,
		Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	h := headers()
	if h == nil {
		t.Fatal("no request reached the server")
	}
	if got, want := h.Get("session-id"), codexSessionID(key); got != want {
		t.Errorf("session-id = %q, want %q (the value derived from the cache key)", got, want)
	}
	if got := h.Get("originator"); got != "terva" {
		t.Errorf("originator = %q, want \"terva\" — this change adds a header, it does not "+
			"adopt another client's identity", got)
	}
	if got := h.Get("user-agent"); !strings.HasPrefix(got, "terva ") {
		t.Errorf("user-agent = %q, want terva's own", got)
	}
}

// The other half: a request with no cache key must send NO session-id header,
// rather than an empty one or a minted one. Without this, the empty-key branch
// could regress to sending a per-request value and every test above would
// still pass.
func TestCodexStreamOmitsSessionIDWithoutACacheKey(t *testing.T) {
	srv, headers := codexCaptureServer(t)
	c := NewOpenAICodex("token", "acct", srv.URL)

	if _, err := c.Stream(context.Background(), Request{
		Model:    "gpt-5.6-sol",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	h := headers()
	if h == nil {
		t.Fatal("no request reached the server")
	}
	if _, present := h["Session-Id"]; present {
		t.Errorf("session-id header present (%q) on a request with no cache key; want it absent",
			h.Get("session-id"))
	}
}
