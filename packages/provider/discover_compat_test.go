package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		// One model with a vLLM-style max_model_len hint, one without
		// (should fall back to the default), and an embedding model that
		// must be filtered out.
		w.Write([]byte(`{"data":[
			{"id":"qwen2.5-coder","max_model_len":131072},
			{"id":"llama3"},
			{"id":"text-embedding-3-small"}
		]}`))
	}))
	defer srv.Close()

	got, err := DiscoverOpenAICompatible(context.Background(), srv.URL, "", 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 chat models, got %d: %+v", len(got), got)
	}
	byID := map[string]Model{}
	for _, m := range got {
		if m.Provider != "openai-compatible" {
			t.Fatalf("provider=%q", m.Provider)
		}
		if m.BaseURL != srv.URL {
			t.Fatalf("baseURL=%q", m.BaseURL)
		}
		byID[m.ID] = m
	}
	if byID["qwen2.5-coder"].ContextWindow != 131072 {
		t.Fatalf("qwen ctx=%d (want server hint 131072)", byID["qwen2.5-coder"].ContextWindow)
	}
	if byID["llama3"].ContextWindow != 8192 {
		t.Fatalf("llama3 ctx=%d (want default 8192)", byID["llama3"].ContextWindow)
	}
}

// The base URL may already include /v1; we must not double it.
func TestDiscoverOpenAICompatibleV1Suffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	if _, err := DiscoverOpenAICompatible(context.Background(), srv.URL+"/v1", "", 4096); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path=%q (want /v1/models)", gotPath)
	}
}
