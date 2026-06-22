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
	// A standard /v1/models entry carries no modality data, so discovery must
	// make an explicit image-input assertion (text-only for these ids) rather
	// than leaving the catalog's optimistic default true.
	if byID["qwen2.5-coder"].Has(CapImageInput) {
		t.Error("qwen2.5-coder should be marked text-only (no vision signal in id)")
	}
}

// looksVisionCapable recognizes known vision families and treats everything
// else as text-only, so a text-only gateway model never gets handed an image.
// Calibrated against the live opencode / opencode-go id sets.
func TestLooksVisionCapable(t *testing.T) {
	vision := []string{
		"claude-opus-4-8", "claude-sonnet-4-5", "claude-haiku-4-5",
		"gpt-5.2", "gpt-5.1-codex", "gpt-4o", "chatgpt-4o-latest",
		"gemini-3-flash", "gemini-3.1-pro",
		"o3-mini", "o1",
		"llava-13b", "qwen2.5-vl-7b", "pixtral-12b", "internvl-2", "moondream2",
	}
	textOnly := []string{
		// opencode-go's set — all text-only; these were the ones that 400'd.
		"minimax-m3", "kimi-k2.5", "qwen3.7-max", "deepseek-v4-pro",
		"glm-5.2", "mimo-v2-omni", "hy3-preview",
		// other plausible text ids
		"qwen2.5-coder", "llama3", "nemotron-3-ultra", "big-pickle",
		"claude-2.1", "claude-instant-1", // pre-vision Claude
	}
	for _, id := range vision {
		if !looksVisionCapable(id) {
			t.Errorf("looksVisionCapable(%q) = false; want true", id)
		}
	}
	for _, id := range textOnly {
		if looksVisionCapable(id) {
			t.Errorf("looksVisionCapable(%q) = true; want false", id)
		}
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
