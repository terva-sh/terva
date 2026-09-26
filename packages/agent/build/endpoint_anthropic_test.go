package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
)

// A named endpoint's `api` field decides which client the registry builds.
//
// The discriminator defaults to OpenAI, and that default is load-bearing rather
// than lazy: every endpoint written before the field existed speaks Chat
// Completions, and the absent value has to keep meaning what it meant when the
// operator wrote it. Both directions are asserted, because a change that made
// everything Anthropic would break silently — an OpenAI endpoint pointed at the
// Messages wire 404s on /v1/messages, long after the registration that caused it.
func TestEndpointAPIFieldSelectsTheClient(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ep       config.EndpointConfig
		wantWire string
	}{
		{"absent api means openai", config.EndpointConfig{BaseURL: "http://box:8000/v1"}, "openai-compat"},
		{"explicit openai", config.EndpointConfig{BaseURL: "http://box:8000/v1", API: "openai"}, "openai-compat"},
		{"anthropic", config.EndpointConfig{BaseURL: "http://box:4000", API: "anthropic"}, "anthropic"},
		// The operator hand-edits config.json, so the value arrives in whatever
		// case they typed. IsAnthropic folds it; a case-sensitive compare here
		// would send "Anthropic" to the OpenAI client with no warning anywhere.
		{"case-folded", config.EndpointConfig{BaseURL: "http://box:4000", API: "Anthropic"}, "anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "wire-probe"
			UnregisterEndpoint(id)
			if err := RegisterOrReplaceEndpoint(id, tc.ep); err != nil {
				t.Fatalf("register: %v", err)
			}
			t.Cleanup(func() { UnregisterEndpoint(id) })

			c := Resolved{Provider: id, Credential: "k", AuthMethod: "apikey", BaseURL: tc.ep.BaseURL}.NewClient()
			if got := provider.ClientReasoningWire(c); got != tc.wantWire {
				t.Errorf("reasoning wire = %q, want %q — the registry built the wrong client for api=%q",
					got, tc.wantWire, tc.ep.API)
			}
			// BOTH wires name themselves after the endpoint, and it is not
			// cosmetic on either: anthropicClient.buildRequest resolves its
			// model with FindModel(c.Name(), …) against rows stamped with the
			// endpoint id, and openaiClient gates prompt_cache_key on
			// `name == "openai"`. A client that called itself "openai" would
			// miss the first and defeat the second.
			if got := c.Name(); got != id {
				t.Errorf("Name() = %q, want the endpoint id %q", got, id)
			}
		})
	}
}

// An Anthropic endpoint's own model rows must be reachable through the name its
// client reports, because that is how buildRequest sizes max_tokens.
//
// This is the second half of the max_tokens trap: discovery stamps a non-zero
// MaxOutput, and it only reaches the request if FindModel can find the row. A
// client naming itself "anthropic-compatible" would look up the shared slot's
// catalog, miss, fall through to the agnostic lookup, and — for a model id the
// baked catalog has never heard of — fail the turn outright.
func TestAnthropicEndpointFindsItsOwnModels(t *testing.T) {
	const id = "lookup-probe"
	UnregisterEndpoint(id)
	if err := RegisterOrReplaceEndpoint(id, config.EndpointConfig{
		BaseURL: "http://box:4000", API: "anthropic",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(id) })

	modelreg.RegisterExtraModel(provider.Model{
		Provider: id, ID: "house-model", DisplayName: "house-model",
		ContextWindow: 200000, MaxOutput: 4096, Source: "live",
	})

	c := Resolved{Provider: id, Credential: "k", AuthMethod: "apikey", BaseURL: "http://box:4000"}.NewClient()
	m, err := modelreg.FindModel(c.Name(), "house-model")
	if err != nil {
		t.Fatalf("FindModel(%q, house-model): %v — the endpoint cannot see its own models", c.Name(), err)
	}
	if m.MaxOutput != 4096 {
		t.Errorf("MaxOutput = %d, want the discovered 4096", m.MaxOutput)
	}
}

// The wire settings reach the constructed client, not just the config struct.
//
// Resolve supplies them for a normal run; the capture inside the registry
// closure is the fallback for a caller building from a bare Resolved. Both are
// exercised here, because the fallback is the path with no test coverage
// anywhere else and the one a future refactor would quietly drop.
func TestEndpointAnthropicOptionsReachTheClient(t *testing.T) {
	ep := config.EndpointConfig{
		BaseURL:          "http://box:4000",
		API:              "anthropic",
		AnthropicVersion: "2099-01-01",
		AnthropicBeta:    "context-1m-2025-08-07",
		AuthStyle:        "bearer",
		DisableCaching:   true,
	}
	got := EndpointAnthropicOptions(ep)
	want := provider.AnthropicCompatOptions{
		APIVersion:     "2099-01-01",
		Beta:           "context-1m-2025-08-07",
		BearerAuth:     true,
		DisableCaching: true,
	}
	if got != want {
		t.Errorf("EndpointAnthropicOptions = %+v, want %+v", got, want)
	}

	// An OpenAI endpoint carrying the fields (a hand-edited config) yields the
	// zero options rather than half-applying them: they are inert, not a
	// silent instruction to the OpenAI client.
	openai := ep
	openai.API = ""
	if o := EndpointAnthropicOptions(openai); !o.IsZero() {
		t.Errorf("EndpointAnthropicOptions on an OpenAI endpoint = %+v, want the zero value", o)
	}
}

// The shared anthropic-compatible slot resolves as a keyless, open-catalogue
// provider — exactly as openai-compatible does. A slot terva refuses to build a
// client for is a login that appears to work and then cannot run a turn.
func TestAnthropicCompatibleSlotIsRegistered(t *testing.T) {
	spec, ok := specFor(provider.AnthropicCompatProvider)
	if !ok {
		t.Fatal("anthropic-compatible is not in the provider registry")
	}
	if !spec.noDefaultModel {
		t.Error("noDefaultModel = false; only the operator knows what their server serves")
	}
	if len(spec.apiKeyEnv) != 0 {
		t.Errorf("apiKeyEnv = %v, want none — the key is optional and lives under this id", spec.apiKeyEnv)
	}
	c := Resolved{
		Provider:   provider.AnthropicCompatProvider,
		Credential: "k",
		AuthMethod: "apikey",
		BaseURL:    "http://localhost:4000",
	}.NewClient()
	if got := provider.ClientReasoningWire(c); got != "anthropic" {
		t.Errorf("reasoning wire = %q, want anthropic", got)
	}
}

// The CompatWire captured at registration is the fallback when Resolve supplies
// none — the path a caller building from a bare Resolved takes, and the one with
// no other coverage.
func TestRegisteredEndpointFallsBackToItsCapturedWire(t *testing.T) {
	const id = "capture-probe"
	UnregisterEndpoint(id)
	if err := RegisterOrReplaceEndpoint(id, config.EndpointConfig{
		BaseURL:   "http://box:4000",
		API:       "anthropic",
		AuthStyle: "bearer",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(id) })

	// No CompatWire on the Resolved: the closure must reach for what it
	// captured rather than silently authenticating the default way. Asserted
	// off a real request, because the setting IS a header — a check against the
	// options struct would pass with the header wiring deleted.
	h := captureEndpointRequest(t, id, provider.AnthropicCompatOptions{})
	if got := h.Get("authorization"); got != "Bearer k" {
		t.Errorf("authorization = %q, want the captured bearer style", got)
	}

	// And an explicit CompatWire still wins, which is the normal Resolve path.
	h = captureEndpointRequest(t, id, provider.AnthropicCompatOptions{APIVersion: "2099-01-01"})
	if got := h.Get("x-api-key"); got != "k" {
		t.Errorf("x-api-key = %q; an explicit CompatWire did not override the captured one", got)
	}
	if got := h.Get("anthropic-version"); got != "2099-01-01" {
		t.Errorf("anthropic-version = %q, want the supplied override", got)
	}
}

// captureEndpointRequest builds the registered endpoint's client against a stub
// server and returns the headers of the one request it sends.
func captureEndpointRequest(t *testing.T, id string, wire provider.AnthropicCompatOptions) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := Resolved{
		Provider:   id,
		Credential: "k",
		AuthMethod: "apikey",
		BaseURL:    srv.URL,
		CompatWire: wire,
	}.NewClient()
	ch, err := c.Stream(context.Background(), provider.Request{
		Model:    "claude-sonnet-4.5",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive // drain
	}
	return got
}

// Only the REAL OpenAI backend is called "openai".
//
// 🪤 ollama, the shared openai-compatible slot, and every named OpenAI endpoint
// were all built with provider.NewOpenAI, which hardcodes that name — so three
// operator-supplied backends claimed to BE OpenAI. The claim drove behaviour:
// openaiClient.buildRequest forwards prompt_cache_key when `name == "openai"`,
// guarded by a comment explaining that the parameter is for the real backend
// only because an unknown one "risks a 400" elsewhere. The guard could not
// fire — the local server it was protecting answered to the same name.
//
// Asserted through the registry rather than the constructors, because the
// registry is where the three were wired and where a regression would land.
func TestOnlyRealOpenAIIsNamedOpenAI(t *testing.T) {
	const epID = "operator-box"
	UnregisterEndpoint(epID)
	if err := RegisterOrReplaceEndpoint(epID, config.EndpointConfig{BaseURL: "http://box:8000/v1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(epID) })

	for _, tc := range []struct{ providerID, wantName string }{
		{"openai", "openai"}, // the genuine article, unchanged
		{"ollama", "ollama"},
		{provider.OpenAICompatProvider, provider.OpenAICompatProvider},
		{epID, epID},
	} {
		c := Resolved{
			Provider: tc.providerID, Credential: "k", AuthMethod: "apikey",
			BaseURL: "https://example.invalid",
		}.NewClient()
		if got := c.Name(); got != tc.wantName {
			t.Errorf("%s: Name() = %q, want %q", tc.providerID, got, tc.wantName)
		}
	}
}

// The behavioural half of the rename: the cache-routing key reaches OpenAI and
// nobody else. A name check alone would not catch someone widening the gate.
func TestPromptCacheKeyReachesOnlyRealOpenAI(t *testing.T) {
	const epID = "operator-box"
	UnregisterEndpoint(epID)
	if err := RegisterOrReplaceEndpoint(epID, config.EndpointConfig{BaseURL: "http://box:8000/v1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(epID) })

	for _, tc := range []struct {
		providerID string
		wantKey    bool
	}{
		{"openai", true},
		{"ollama", false},
		{provider.OpenAICompatProvider, false},
		{epID, false},
	} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("content-type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		c := Resolved{
			Provider: tc.providerID, Credential: "k", AuthMethod: "apikey", BaseURL: srv.URL,
		}.NewClient()
		ch, err := c.Stream(context.Background(), provider.Request{
			Model:          "some-model",
			PromptCacheKey: "sess-1",
			Messages:       []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}},
		})
		if err != nil {
			srv.Close()
			t.Fatalf("%s: Stream: %v", tc.providerID, err)
		}
		for range ch { //nolint:revive // drain
		}
		srv.Close()

		_, sent := body["prompt_cache_key"]
		if sent != tc.wantKey {
			t.Errorf("%s: prompt_cache_key sent = %v, want %v — an operator's own server "+
				"may reject an unknown parameter with a 400", tc.providerID, sent, tc.wantKey)
		}
	}
}
