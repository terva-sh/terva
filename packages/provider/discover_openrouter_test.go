package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDiscoverOpenRouterPrefersServedContextLength verifies the
// discovery fix: OpenRouter reports an inflated model-level
// context_length (the model's theoretical max) alongside the serving
// provider's real top_provider.context_length. When the served limit is
// smaller, discovery must use it, because that's the limit OpenRouter
// actually enforces. Using the inflated value made every context-window
// check useless and let max_tokens exceed the real ceiling.
func TestDiscoverOpenRouterPrefersServedContextLength(t *testing.T) {
	const body = `{"data":[
		{"id":"infl","name":"Inflated","context_length":1000000,
		 "top_provider":{"context_length":262144}},
		{"id":"served-bigger","name":"ServedBigger","context_length":131072,
		 "top_provider":{"context_length":262144}},
		{"id":"no-top","name":"NoTop","context_length":128000,
		 "top_provider":{"context_length":0}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := DiscoverOpenRouter(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// Inflated model-level window: use the smaller served limit.
	if got := byID["infl"].ContextWindow; got != 262144 {
		t.Errorf("inflated model ContextWindow = %d; want 262144 (served limit)", got)
	}
	// Served limit larger than model-level: keep the smaller model value.
	if got := byID["served-bigger"].ContextWindow; got != 131072 {
		t.Errorf("served-bigger ContextWindow = %d; want 131072 (model value, the smaller one)", got)
	}
	// No served limit: fall back to the model-level value.
	if got := byID["no-top"].ContextWindow; got != 128000 {
		t.Errorf("no-top ContextWindow = %d; want 128000 (model value, no top_provider)", got)
	}
}

// OpenRouter's architecture.input_modalities / output_modalities are
// the one modality signal among the endpoints terva already queries;
// they become explicit capability assertions. A missing architecture
// block asserts nothing, leaving the per-capability defaults in force.
func TestDiscoverOpenRouterModalities(t *testing.T) {
	const body = `{"data":[
		{"id":"vision","name":"V","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}},
		{"id":"text-only","name":"T","architecture":{"input_modalities":["text"],"output_modalities":["text"]}},
		{"id":"image-gen","name":"G","architecture":{"input_modalities":["text"],"output_modalities":["text","image"]}},
		{"id":"no-arch","name":"N"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	models, err := DiscoverOpenRouter(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}

	if m := byID["vision"]; !m.Has(CapImageInput) || m.Has(CapImageOutput) {
		t.Errorf("vision caps = %v", m.Caps)
	}
	if m := byID["text-only"]; m.Has(CapImageInput) {
		t.Errorf("text-only caps = %v, want explicit image-input:false", m.Caps)
	}
	if m := byID["image-gen"]; !m.Has(CapImageOutput) {
		t.Errorf("image-gen caps = %v, want image-output:true", m.Caps)
	}
	if m := byID["no-arch"]; m.Caps != nil {
		t.Errorf("no-arch caps = %v, want nil (nothing asserted)", m.Caps)
	}
}
