package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pngBytes carries the PNG signature so http.DetectContentType returns
// image/png; the rest is filler standing in for pixel data.
var pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("pixels")...)

func TestOpenAIImagesGenerate(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	var got oaiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q, want /images/generations", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprintf(w, `{"data":[{"b64_json":%q},{"b64_json":%q}]}`, b64, b64)
	}))
	defer srv.Close()

	b := &OpenAIImages{Name: "openai-images", BaseURL: srv.URL, APIKey: "sk-test", Model: "gpt-image-1", DefaultSize: "1024x1024"}
	res, err := b.Generate(context.Background(), Request{Prompt: "a cat", N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "a cat" || got.N != 2 || got.Size != "1024x1024" || got.Model != "gpt-image-1" {
		t.Errorf("request = %+v", got)
	}
	if len(res.Images) != 2 {
		t.Fatalf("images = %d, want 2", len(res.Images))
	}
	if res.Images[0].MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", res.Images[0].MimeType)
	}
	if res.Backend != "openai-images" || res.Model != "gpt-image-1" {
		t.Errorf("result meta = %+v", res)
	}
}

func TestOpenAIImagesNegativePipe(t *testing.T) {
	var got oaiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString(pngBytes))
	}))
	defer srv.Close()
	b := &OpenAIImages{Name: "localai", BaseURL: srv.URL, Model: "sdxl", NegativePipe: true}
	if _, err := b.Generate(context.Background(), Request{Prompt: "a fox", NegativePrompt: "blurry"}); err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "a fox|blurry" {
		t.Errorf("piped prompt = %q, want a fox|blurry", got.Prompt)
	}
}

func TestOpenAIImagesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"content policy violation"}}`)
	}))
	defer srv.Close()
	b := &OpenAIImages{Name: "openai-images", BaseURL: srv.URL, Model: "gpt-image-1"}
	_, err := b.Generate(context.Background(), Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "content policy violation") {
		t.Fatalf("want the API error surfaced, got %v", err)
	}
}

func TestRegistryResolve(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Resolve(""); err == nil {
		t.Error("empty registry should not resolve a default")
	}
	r.Add(&OpenAIImages{Name: "a"})
	if b, err := r.Resolve(""); err != nil || b.ID() != "a" {
		t.Errorf("sole backend should resolve: %v", err)
	}
	r.Add(&OpenAIImages{Name: "b"})
	if _, err := r.Resolve(""); err == nil {
		t.Error("two backends with no default should not resolve a default")
	}
	if b, err := r.Resolve("b"); err != nil || b.ID() != "b" {
		t.Errorf("explicit id should resolve: %v", err)
	}
	if _, err := r.Resolve("nope"); err == nil {
		t.Error("unknown id should error")
	}
	r.SetDefault("b")
	if b, err := r.Resolve(""); err != nil || b.ID() != "b" {
		t.Errorf("default should resolve to b: %v", err)
	}
}
