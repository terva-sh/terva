package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The four knobs an Anthropic-compatible endpoint exists to expose.
//
// Each is here because a real class of server fails EVERY turn without it, with
// an error naming none of them — so each test asserts the thing that reaches the
// wire, not the field that was set.

// anthCompatCapture runs one Stream against a server that records the request
// and then closes the body, returning the captured headers and decoded body.
//
// A real round trip rather than a buildRequest call, because three of the four
// settings are HEADERS: a test that only inspected the request struct would pass
// with the header wiring deleted.
func anthCompatCapture(t *testing.T, key string, o AnthropicCompatOptions, req Request) (http.Header, map[string]any) {
	t.Helper()
	var gotHeader http.Header
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := NewAnthropicCompatOpts("my-gateway", key, srv.URL, o)
	if req.Model == "" {
		req.Model = "claude-sonnet-4.5"
	}
	if len(req.Messages) == 0 {
		req.Messages = []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}
	}
	ch, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive // drain
	}
	if gotBody == nil {
		t.Fatal("server received no decodable body")
	}
	return gotHeader, gotBody
}

// The default is Anthropic's own convention, and the alternative is what a
// gateway fronting Claude with an OpenAI-style door wants. Getting this wrong is
// a 401 that names neither header.
func TestAnthropicCompatAuthStyle(t *testing.T) {
	t.Run("x-api-key by default", func(t *testing.T) {
		h, _ := anthCompatCapture(t, "sk-secret", AnthropicCompatOptions{}, Request{})
		if got := h.Get("x-api-key"); got != "sk-secret" {
			t.Errorf("x-api-key = %q, want the key", got)
		}
		if got := h.Get("authorization"); got != "" {
			t.Errorf("authorization = %q, want it unset", got)
		}
	})
	t.Run("bearer when asked", func(t *testing.T) {
		h, _ := anthCompatCapture(t, "sk-secret", AnthropicCompatOptions{BearerAuth: true}, Request{})
		if got := h.Get("authorization"); got != "Bearer sk-secret" {
			t.Errorf("authorization = %q, want a bearer token", got)
		}
		// Both headers together is not "belt and braces": a gateway that
		// authenticates on the first one it recognises would silently use a
		// credential the operator did not select.
		if got := h.Get("x-api-key"); got != "" {
			t.Errorf("x-api-key = %q, want it unset under bearer auth", got)
		}
	})
}

// Bearer auth must NOT drag in the Claude Code costume. That is what the
// separate `oauth` flag means, and conflating the two would advertise terva as
// Anthropic's official CLI to a third-party server and rename the operator's
// tools underneath them.
func TestAnthropicCompatBearerIsNotTheSubscriptionMode(t *testing.T) {
	h, body := anthCompatCapture(t, "sk", AnthropicCompatOptions{BearerAuth: true}, Request{
		System: "You are a helpful assistant.",
		Tools:  []Tool{{Name: "read", Description: "read a file"}},
	})
	if got := h.Get("anthropic-beta"); strings.Contains(got, "claude-code") {
		t.Errorf("anthropic-beta = %q, want no claude-code opt-in", got)
	}
	if got := h.Get("user-agent"); strings.Contains(got, "claude-cli") {
		t.Errorf("user-agent = %q, want terva not to claim to be claude-cli", got)
	}
	// The identity system block is the subscription's, and prepending it here
	// would spend a cache breakpoint on a sentence this server never asked for.
	if sys, _ := json.Marshal(body["system"]); strings.Contains(string(sys), claudeCodeIdentity) {
		t.Errorf("system = %s, want no Claude Code identity block", sys)
	}
	// Tool names stay as terva spells them: the renaming exists because
	// Anthropic's backend cross-checks the official CLI's casing, which a
	// third-party server does not.
	tools, _ := json.Marshal(body["tools"])
	if !strings.Contains(string(tools), `"read"`) {
		t.Errorf("tools = %s, want the tool name unchanged", tools)
	}
}

func TestAnthropicCompatVersionAndBetaHeaders(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		h, _ := anthCompatCapture(t, "k", AnthropicCompatOptions{}, Request{})
		if got := h.Get("anthropic-version"); got != AnthropicDefaultAPIVersion {
			t.Errorf("anthropic-version = %q, want %q", got, AnthropicDefaultAPIVersion)
		}
		if got := h.Get("anthropic-beta"); got != "" {
			t.Errorf("anthropic-beta = %q, want it unset", got)
		}
	})
	t.Run("overridden", func(t *testing.T) {
		h, _ := anthCompatCapture(t, "k", AnthropicCompatOptions{
			APIVersion: "2099-01-01",
			Beta:       "context-1m-2025-08-07,fine-grained-tool-streaming-2025-05-14",
		}, Request{})
		if got := h.Get("anthropic-version"); got != "2099-01-01" {
			t.Errorf("anthropic-version = %q, want the override", got)
		}
		if got := h.Get("anthropic-beta"); !strings.Contains(got, "context-1m") {
			t.Errorf("anthropic-beta = %q, want the operator's flags", got)
		}
	})
}

// Prompt caching off must remove EVERY breakpoint. A request carrying three of
// its four is still a 400 against a server that validates the body strictly,
// and would look like the setting did nothing.
func TestAnthropicCompatDisableCachingRemovesEveryBreakpoint(t *testing.T) {
	req := Request{
		System:   "You are a helpful assistant.",
		Tools:    []Tool{{Name: "read", Description: "read a file"}},
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hello there"}}}},
	}

	_, on := anthCompatCapture(t, "k", AnthropicCompatOptions{}, req)
	raw, _ := json.Marshal(on)
	if !strings.Contains(string(raw), "cache_control") {
		t.Fatal("caching ON sent no cache_control; this test would pass vacuously")
	}

	_, off := anthCompatCapture(t, "k", AnthropicCompatOptions{DisableCaching: true}, req)
	raw, _ = json.Marshal(off)
	if strings.Contains(string(raw), "cache_control") {
		t.Errorf("caching OFF still sent cache_control: %s", raw)
	}
}

// Discovery reads Anthropic's page shape (data[].id + display_name, paged by
// has_more/last_id), which is what separates it from the OpenAI discoverer
// beyond the auth header.
func TestDiscoverAnthropicCompatiblePagesAndStamps(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("authorization")
		w.Header().Set("content-type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-4.5","display_name":"Claude Sonnet 4.5"}],
				"has_more":true,"last_id":"claude-sonnet-4.5"}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"local-llama"}],"has_more":false}`)
	}))
	defer srv.Close()

	got, err := DiscoverAnthropicCompatible(context.Background(), srv.URL, "sk", 200000,
		AnthropicCompatOptions{BearerAuth: true})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want both pages", len(got))
	}
	if sawAuth != "Bearer sk" {
		t.Errorf("discovery authorization = %q; it must authenticate the way the real request does", sawAuth)
	}
	if got[0].DisplayName != "Claude Sonnet 4.5" {
		t.Errorf("display name = %q, want the server's", got[0].DisplayName)
	}
	// A row with no display_name falls back to its id rather than rendering blank.
	if got[1].DisplayName != "local-llama" {
		t.Errorf("display name = %q, want the id as fallback", got[1].DisplayName)
	}
	for _, m := range got {
		if m.Provider != AnthropicCompatProvider {
			t.Errorf("%s: provider = %q", m.ID, m.Provider)
		}
		if m.ContextWindow != 200000 {
			t.Errorf("%s: context window = %d, want the supplied default", m.ID, m.ContextWindow)
		}
		// 🪤 The one that breaks every turn rather than one field: the Messages
		// API requires max_tokens, and buildRequest falls back to MaxOutput. A
		// zero here means `"max_tokens": 0` on every request.
		if m.MaxOutput <= 0 {
			t.Errorf("%s: MaxOutput = %d, want a non-zero floor — the Messages wire requires max_tokens", m.ID, m.MaxOutput)
		}
	}
}

// A server without /v1/models is a normal, working endpoint: the operator typed
// a default model, and discovery is a convenience on top. It must report the
// failure rather than panic or invent rows.
func TestDiscoverAnthropicCompatibleToleratesNoModelsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := DiscoverAnthropicCompatible(context.Background(), srv.URL, "", 0, AnthropicCompatOptions{})
	if err == nil {
		t.Fatal("want an error for a 404 model list")
	}
	if len(got) != 0 {
		t.Errorf("got %d models alongside the error", len(got))
	}
}

// The shared slot and a named endpoint must be distinguishable by Name(): it is
// what cost lookup, the rescue picker, and every error message route on, and an
// operator with three Anthropic endpoints has to be told which one refused them.
func TestAnthropicCompatNamesItself(t *testing.T) {
	if got := NewAnthropicCompatible("k", "https://example.invalid", AnthropicCompatOptions{}).Name(); got != AnthropicCompatProvider {
		t.Errorf("shared slot Name() = %q, want %q", got, AnthropicCompatProvider)
	}
	if got := NewAnthropicCompatOpts("workshop-box", "k", "https://example.invalid", AnthropicCompatOptions{}).Name(); got != "workshop-box" {
		t.Errorf("named endpoint Name() = %q, want the endpoint id", got)
	}
}

// The compatible slots are recognised as endpoint-shaped logins. Both of them:
// the predicate exists because the equality it replaced was written at ~eight
// sites while there was only one such provider.
func TestIsCompatProvider(t *testing.T) {
	for _, id := range []string{OpenAICompatProvider, AnthropicCompatProvider} {
		if !IsCompatProvider(id) {
			t.Errorf("IsCompatProvider(%q) = false", id)
		}
	}
	for _, id := range []string{"anthropic", "openai", "ollama", "my-endpoint", ""} {
		if IsCompatProvider(id) {
			t.Errorf("IsCompatProvider(%q) = true", id)
		}
	}
}
