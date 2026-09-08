package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// opencodeCaptureServer stands in for the Zen gateway and hands back the
// headers of the request it received.
func opencodeCaptureServer(t *testing.T) (*httptest.Server, func() http.Header) {
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

func opencodeStream(t *testing.T, c Client, cacheKey string) {
	t.Helper()
	if _, err := c.Stream(context.Background(), Request{
		Model:          "glm-5.2",
		PromptCacheKey: cacheKey,
		Messages:       []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
}

// The failure this guards: without the header the gateway answers
//
//	opencode-go: http 400: {"type":"error","error":{"type":"MissingSessionID",
//	"message":"… Request is missing x-opencode-session …"}}
//
// and the turn produces no answer at all. Presence is therefore the whole
// bug, and the user agent rides along because the same docs page asks a
// client not to present as its HTTP library.
func TestOpenCodeGoStreamSendsSessionHeaderAndUserAgent(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	srv, headers := opencodeCaptureServer(t)
	opencodeStream(t, NewOpenCodeGo("sk-test", srv.URL+"/v1"), key)

	h := headers()
	if h == nil {
		t.Fatal("no request reached the server")
	}
	if got, want := h.Get("x-opencode-session"), opencodeSessionID(key); got != want {
		t.Errorf("x-opencode-session = %q, want %q (the value derived from the cache key)", got, want)
	}
	if got := h.Get("user-agent"); !strings.HasPrefix(got, "terva/") {
		t.Errorf("user-agent = %q, want terva's own. Go's default Go-http-client/<ver> is the "+
			"generic HTTP-library name opencode's docs ask clients not to send", got)
	}
}

// `opencode` runs through the same gateway, so the same header applies. It
// is a separate constructor and a separate registry entry, which is exactly
// how one of the two gets fixed and the other forgotten.
func TestOpenCodeStreamSendsSessionHeader(t *testing.T) {
	srv, headers := opencodeCaptureServer(t)
	opencodeStream(t, NewOpenCode("sk-test", srv.URL+"/v1"), "some-session")

	if got := headers().Get("x-opencode-session"); got == "" {
		t.Error("x-opencode-session absent on the opencode provider")
	}
}

// Presence alone is satisfied by a fresh id per request, which would clear
// the 400 while defeating the routing and prompt-cache locality the header
// exists to buy. Stability across a conversation's dispatches is the
// property that makes the header worth sending.
func TestOpenCodeSessionHeaderIsStableAcrossDispatches(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	srvA, headersA := opencodeCaptureServer(t)
	srvB, headersB := opencodeCaptureServer(t)

	// Deliberately two clients rather than two calls on one. The id has to
	// follow the CONVERSATION, so a --resume, a model swap, or any other
	// rebuild of the provider binding must rejoin the same route. Two calls
	// on one client would pass even if the id were minted per client.
	opencodeStream(t, NewOpenCodeGo("sk-test", srvA.URL+"/v1"), key)
	opencodeStream(t, NewOpenCodeGo("sk-test", srvB.URL+"/v1"), key)

	first, second := headersA().Get("x-opencode-session"), headersB().Get("x-opencode-session")
	if first == "" {
		t.Fatal("x-opencode-session absent")
	}
	if first != second {
		t.Errorf("x-opencode-session changed between dispatches of one conversation: %q then %q",
			first, second)
	}
}

func TestOpenCodeSessionIDIsDistinctPerConversation(t *testing.T) {
	seen := map[string]string{}
	for _, key := range []string{
		"0e3c4d40-b787-8bcc-9f12-20260804154657",
		"20260804-195015-dd0aff9f", // legacy transcripts key on a file basename
		"live-9a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"session.json", // every swarm child's transcript is named this
	} {
		got := opencodeSessionID(key)
		if _, err := uuid.Parse(got); err != nil {
			t.Fatalf("opencodeSessionID(%q) = %q, which is not a UUID: %v", key, got, err)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("opencodeSessionID collided: %q and %q both gave %q", prev, key, got)
		}
		seen[got] = key
	}
}

// The Codex derivation over the same cache key must land somewhere else.
// Two namespaces exist so one gateway's session id can never be another's.
func TestOpenCodeSessionIDDiffersFromCodex(t *testing.T) {
	const key = "0e3c4d40-b787-8bcc-9f12-20260804154657"
	if opencodeSessionID(key) == codexSessionID(key) {
		t.Error("opencode and codex derive the same session id from one cache key")
	}
}

// Codex omits its header when there is no cache key, because a random id
// there would measure like the baseline while presenting as the fix. Here
// omitting it means a 400 and no answer, so the empty case must still send
// something, and that something must be stable for the client's life rather
// than minted per request.
func TestOpenCodeSessionHeaderSurvivesAnEmptyCacheKey(t *testing.T) {
	c := NewOpenCodeGo("sk-test", "")
	inner, ok := c.(*openaiClient)
	if !ok {
		t.Fatalf("NewOpenCodeGo returned %T, want *openaiClient", c)
	}
	first := inner.sessionID("")
	if first == "" {
		t.Fatal("empty cache key produced no session id; the gateway answers 400 to that")
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("fallback session id %q is not a UUID: %v", first, err)
	}
	for i := 0; i < 4; i++ {
		if got := inner.sessionID(""); got != first {
			t.Fatalf("fallback session id is not stable: call %d gave %q, first gave %q",
				i+2, got, first)
		}
	}
	// Two clients are two conversations as far as the gateway can tell, so
	// they must not share the fallback.
	other := NewOpenCodeGo("sk-test", "").(*openaiClient)
	if other.sessionID("") == first {
		t.Error("two clients share one fallback session id; concurrent conversations would " +
			"collide on one route")
	}
}

// The session header and the user agent hang off openaiClient, which serves
// every OpenAI-compatible provider. Neither may leak to the others: an
// unexpected header is how a strict endpoint starts answering 400.
func TestNonOpenCodeCompatSendsNoSessionHeader(t *testing.T) {
	srv, headers := opencodeCaptureServer(t)
	opencodeStream(t, NewMoonshot("sk-test", srv.URL+"/v1"), "0e3c4d40-b787-8bcc-9f12-20260804154657")

	h := headers()
	if got, present := h["X-Opencode-Session"]; present {
		t.Errorf("x-opencode-session = %v on moonshotai; the header is opencode's alone", got)
	}
	if got := h.Get("user-agent"); strings.HasPrefix(got, "terva/") {
		t.Errorf("user-agent = %q on moonshotai; only the opencode clients override it", got)
	}
}
