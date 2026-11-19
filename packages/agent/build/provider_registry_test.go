package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// Every registered spec is internally consistent and constructable.
func TestProviderRegistryWellFormed(t *testing.T) {
	for _, spec := range providerSpecs {
		if spec.id == "" {
			t.Error("spec with empty id")
		}
		if spec.newClient == nil {
			t.Errorf("%q has no newClient", spec.id)
		}
		if spec.defaultModel != "" && spec.noDefaultModel {
			t.Errorf("%q sets both defaultModel and noDefaultModel", spec.id)
		}
		if _, ok := specFor(spec.id); !ok {
			t.Errorf("%q missing from the id index", spec.id)
		}
	}
	// ProviderIDs is the registry's id list: the static specs come first, in
	// order, followed by any dynamically-registered endpoints (RegisterEndpoint
	// appends — and other tests in this process may have added some).
	ids := ProviderIDs()
	if len(ids) < len(providerSpecs) {
		t.Fatalf("knownProviders has %d ids, fewer than the %d static specs", len(ids), len(providerSpecs))
	}
	for i, spec := range providerSpecs {
		if ids[i] != spec.id {
			t.Errorf("knownProviders[%d] = %q, want %q (static order must match registry)", i, ids[i], spec.id)
		}
	}
}

// Every alias resolves to a real canonical id and never collides with
// a canonical id or another alias.
func TestProviderRegistryAliasesResolve(t *testing.T) {
	for alias, canon := range providerAliases {
		if !IsKnownProvider(canon) {
			t.Errorf("alias %q maps to unknown id %q", alias, canon)
		}
		if IsKnownProvider(alias) {
			t.Errorf("alias %q collides with a canonical id", alias)
		}
		if canonicalProvider(alias) != canon {
			t.Errorf("canonicalProvider(%q) = %q, want %q", alias, canonicalProvider(alias), canon)
		}
	}
}

// NewClient builds a non-nil client of the expected provider name for
// every registered provider, driving the dispatch end to end with a
// dummy credential. (Bedrock/codex/vertex/azure constructors that need
// real credentials still return a usable client object — we only
// assert non-nil and that Name() is stable where the client exposes
// the canonical id.)
func TestProviderRegistryNewClientDispatch(t *testing.T) {
	// Providers whose client.Name() is a stable, registry-matching
	// string. Others wrap or rename (kimi-coding speaks anthropic,
	// gateways relabel), so we only assert non-nil for those.
	wantName := map[string]string{
		"anthropic": "anthropic",
		"openai":    "openai",
		"deepseek":  "deepseek",
		"google":    "google",
	}
	for _, spec := range providerSpecs {
		r := Resolved{Provider: spec.id, Credential: "dummy-key", AuthMethod: "apikey"}
		c := r.NewClient()
		if c == nil {
			t.Errorf("%q: NewClient returned nil", spec.id)
			continue
		}
		if want, ok := wantName[spec.id]; ok {
			if got := c.Name(); got != want {
				t.Errorf("%q: client Name() = %q, want %q", spec.id, got, want)
			}
		}
	}
}

// DefaultModelForProvider reads the registry: explicit defaults win,
// no-default providers return empty, unknown falls back to the global.
func TestProviderRegistryDefaultModel(t *testing.T) {
	if got := DefaultModelForProvider("openai"); got != "gpt-5.2" {
		t.Errorf("openai default = %q, want gpt-5.2", got)
	}
	if got := DefaultModelForProvider("openrouter"); got != "anthropic/claude-sonnet-5.5" {
		t.Errorf("openrouter default = %q, want anthropic/claude-sonnet-5.5", got)
	}
	if got := DefaultModelForProvider("ollama"); got != "" {
		t.Errorf("ollama default = %q, want empty", got)
	}
	if got := DefaultModelForProvider("openai-compatible"); got != "" {
		t.Errorf("openai-compatible default = %q, want empty", got)
	}
	// anthropic names its own default, so it must NOT track the global one.
	// Asserting the id rather than "not the global" is what would catch the
	// default being dropped back to a fall-through.
	if got := DefaultModelForProvider("anthropic"); got != "claude-opus-5-5" {
		t.Errorf("anthropic default = %q, want claude-opus-5-5", got)
	}
	// Both Responses routes default to GPT-6 Sol. Plain openai stays on
	// GPT-5.2 because GPT-6 tool calls require Responses.
	for _, id := range []string{"openai-codex", "openai-responses"} {
		if got := DefaultModelForProvider(id); got != "gpt-6-sol" {
			t.Errorf("%s default = %q, want gpt-6-sol", id, got)
		}
	}
	if got := DefaultModelForProvider("totally-unknown"); got != provider.DefaultModel.ID {
		t.Errorf("unknown default = %q, want global %q", got, provider.DefaultModel.ID)
	}
}

func TestReplacementDefaultsToolRoundTrip(t *testing.T) {
	for _, prov := range []string{"openai", "openrouter"} {
		t.Run(prov, func(t *testing.T) {
			id := DefaultModelForProvider(prov)
			m, err := provider.Builtin().FindModel(prov, id)
			if err != nil || m.Speculative {
				t.Fatalf("default unavailable offline: %+v, %v", m, err)
			}
			if m.MaxOutput != 128000 {
				t.Fatalf("default output limit = %d, want 128000", m.MaxOutput)
			}
			requests := make(chan map[string]any, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				messages, ok := body["messages"].([]any)
				if !ok || len(messages) == 0 {
					t.Errorf("messages must be a nonempty array: %+v", body)
					http.Error(w, "invalid messages", http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				if len(messages) == 1 {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			client := (Resolved{Provider: prov, Credential: "test-key", AuthMethod: "apikey", BaseURL: srv.URL + "/v1"}).NewClient()
			provider.WithCatalog(client, provider.Builtin())
			req := provider.Request{Model: id, Reasoning: "high", ReasoningSet: true,
				Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Look it up"}}}},
				Tools:    []provider.Tool{{Name: "lookup", Description: "Look up a value", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for turn := 0; turn < 2; turn++ {
				stream, err := client.Stream(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				var done *provider.EventDone
				for event := range stream {
					if e, ok := event.(provider.EventDone); ok {
						done = &e
					}
				}
				if done == nil || done.Err != nil {
					t.Fatalf("stream failed: %+v", done)
				}
				body := <-requests
				if body["model"] != id || body["reasoning_effort"] != "high" || body["max_completion_tokens"] != float64(m.MaxOutput) {
					t.Fatalf("incorrect default request: %+v", body)
				}
				if tools, ok := body["tools"].([]any); !ok || len(tools) != 1 {
					t.Fatalf("missing tool definition: %+v", body)
				}
				if turn == 0 {
					if len(done.Message.Content) != 1 {
						t.Fatalf("missing assembled tool call: %+v", done.Message)
					}
					call, ok := done.Message.Content[0].(provider.ToolCallBlock)
					if !ok || call.ID != "call_1" || call.Name != "lookup" || string(call.Arguments) != "{}" {
						t.Fatalf("invalid tool call: %+v", done.Message)
					}
					req.Messages = append(req.Messages, done.Message, provider.Message{Role: provider.RoleTool,
						Content: []provider.Content{provider.ToolResultBlock{CallID: call.ID, Content: []provider.Content{provider.TextBlock{Text: "42"}}}}})
				} else {
					messages := body["messages"].([]any)
					last, ok := messages[len(messages)-1].(map[string]any)
					if !ok {
						t.Fatalf("tool result must be a message object: %+v", messages)
					}
					if last["role"] != "tool" || last["tool_call_id"] != "call_1" || last["content"] != "42" {
						t.Fatalf("tool result lost on follow-up: %+v", messages)
					}
					if len(done.Message.Content) != 1 || done.Message.Content[0] != (provider.TextBlock{Text: "done"}) {
						t.Fatalf("missing final text: %+v", done.Message)
					}
				}
			}
		})
	}
}
