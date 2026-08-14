package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every `google` row in the built-in catalog carries a BaseURL that already
// ends in "/v1beta", and that value reaches the client. Appending the version
// prefix unconditionally built "/v1beta/v1beta/models/…", which Google answers
// with a 404 carrying an EMPTY body — so terva printed a bare "http 404:" and
// every catalogued Gemini model failed before it ever reached the API.
// Measured live 2026-08-14: correct path 200, doubled path 404/0 bytes.
func TestGeminiAPIURLDoesNotDoubleVersionSegment(t *testing.T) {
	const rel = "models/gemini-3.5-flash:streamGenerateContent"
	cases := []struct {
		name string
		base string
		want string
	}{{
		name: "bare host gets the default version prefix",
		base: "https://generativelanguage.googleapis.com",
		want: "https://generativelanguage.googleapis.com/v1beta/" + rel,
	}, {
		// The exact shape every google catalog row carries.
		name: "catalog base already names v1beta",
		base: "https://generativelanguage.googleapis.com/v1beta",
		want: "https://generativelanguage.googleapis.com/v1beta/" + rel,
	}, {
		name: "trailing slash is not a second segment",
		base: "https://generativelanguage.googleapis.com/v1beta/",
		want: "https://generativelanguage.googleapis.com/v1beta/" + rel,
	}, {
		name: "stable v1 is respected, not overridden",
		base: "https://generativelanguage.googleapis.com/v1",
		want: "https://generativelanguage.googleapis.com/v1/" + rel,
	}, {
		name: "v1alpha is respected",
		base: "https://generativelanguage.googleapis.com/v1alpha",
		want: "https://generativelanguage.googleapis.com/v1alpha/" + rel,
	}, {
		// A proxy or gateway with no version in its path still gets one.
		name: "proxy host gets the default version prefix",
		base: "http://127.0.0.1:8788",
		want: "http://127.0.0.1:8788/v1beta/" + rel,
	}, {
		name: "empty base falls back to the default host",
		base: "",
		want: geminiDefaultBaseURL + "/v1beta/" + rel,
	}}
	for _, c := range cases {
		got := geminiAPIURL(c.base, rel)
		if got != c.want {
			t.Errorf("%s:\n got:  %s\n want: %s", c.name, got, c.want)
		}
		if strings.Contains(got, "/v1beta/v1beta") || strings.Contains(got, "/v1/v1") {
			t.Errorf("%s: doubled version segment: %s", c.name, got)
		}
	}
}

// End to end through Stream: a client constructed with the catalog's own base
// URL must request the single-prefix path. This is the regression the live
// probe caught, at the layer the user actually exercises.
func TestGeminiStreamRequestsSinglePrefixedPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer srv.Close()

	// Mirrors the catalog: the base URL already carries its version segment.
	c := NewGemini("k", srv.URL+"/v1beta")
	evs, err := c.Stream(context.Background(), Request{
		Model:    "gemini-3.5-flash",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range evs {
	}
	want := "/v1beta/models/gemini-3.5-flash:streamGenerateContent"
	if gotPath != want {
		t.Fatalf("request path:\n got:  %s\n want: %s", gotPath, want)
	}
}

// Discovery shares the same joiner; a versioned base must not double there
// either, or the model picker comes back empty for the same invisible reason.
func TestDiscoverGoogleDoesNotDoubleVersionSegment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{
				"name":                       "models/gemini-3.5-flash",
				"displayName":                "Gemini 3.5 Flash",
				"inputTokenLimit":            1048576,
				"outputTokenLimit":           65536,
				"supportedGenerationMethods": []string{"generateContent"},
			}},
		})
	}))
	defer srv.Close()

	if _, err := DiscoverGoogle(context.Background(), "k", srv.URL+"/v1beta"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models" {
		t.Fatalf("discovery path = %s, want /v1beta/models", gotPath)
	}
}
