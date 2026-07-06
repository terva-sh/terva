package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestA1111Generate(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	var got a1111Request
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sdapi/v1/txt2img" {
			t.Errorf("path = %q, want /sdapi/v1/txt2img", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprintf(w, `{"images":[%q]}`, b64)
	}))
	defer srv.Close()

	b := &A1111{
		Name: "a1111", BaseURL: srv.URL, APIKey: "user:pass", Model: "sdxl.safetensors",
		DefaultSize: "768x768", Steps: 30, Sampler: "DPM++ 2M", CFGScale: 7,
	}
	res, err := b.Generate(context.Background(), Request{Prompt: "a castle", NegativePrompt: "blurry", Size: "1024x1024"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "a castle" || got.NegativePrompt != "blurry" {
		t.Errorf("prompt/negative = %+v", got)
	}
	if got.Width != 1024 || got.Height != 1024 {
		t.Errorf("dims = %dx%d, want 1024x1024 (request size wins)", got.Width, got.Height)
	}
	if got.Steps != 30 || got.SamplerName != "DPM++ 2M" || got.CFGScale != 7 {
		t.Errorf("sampling = %+v", got)
	}
	if got.OverrideSettings["sd_model_checkpoint"] != "sdxl.safetensors" {
		t.Errorf("checkpoint override = %v", got.OverrideSettings)
	}
	if authHeader != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")) {
		t.Errorf("basic auth = %q", authHeader)
	}
	if len(res.Images) != 1 || res.Images[0].MimeType != "image/png" {
		t.Fatalf("images = %+v", res.Images)
	}
}

func TestA1111DimsFallback(t *testing.T) {
	var got a1111Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprintf(w, `{"images":[%q]}`, base64.StdEncoding.EncodeToString(pngBytes))
	}))
	defer srv.Close()
	// No request size, no default → 512x512.
	b := &A1111{Name: "a1111", BaseURL: srv.URL}
	if _, err := b.Generate(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if got.Width != 512 || got.Height != 512 {
		t.Errorf("fallback dims = %dx%d, want 512x512", got.Width, got.Height)
	}
}

func TestA1111DataURLPrefixStripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Some forks return a data URL rather than bare base64.
		fmt.Fprintf(w, `{"images":["data:image/png;base64,%s"]}`, base64.StdEncoding.EncodeToString(pngBytes))
	}))
	defer srv.Close()
	b := &A1111{Name: "a1111", BaseURL: srv.URL}
	res, err := b.Generate(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("data-url image should decode: %v", err)
	}
	if len(res.Images) != 1 || res.Images[0].MimeType != "image/png" {
		t.Errorf("images = %+v", res.Images)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		w, h int
		ok   bool
	}{
		{"1024x1024", 1024, 1024, true},
		{"1536x1024", 1536, 1024, true},
		{"800X600", 800, 600, true},
		{"", 0, 0, false},
		{"1024", 0, 0, false},
		{"axb", 0, 0, false},
		{"1024x", 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := parseSize(c.in)
		if ok != c.ok || (ok && (w != c.w || h != c.h)) {
			t.Errorf("parseSize(%q) = %d,%d,%v; want %d,%d,%v", c.in, w, h, ok, c.w, c.h, c.ok)
		}
	}
}
