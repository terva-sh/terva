package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBodyCapped(t *testing.T) {
	// Under the cap: full body, not truncated.
	body, truncated := readBodyCapped(strings.NewReader("hello"), 64)
	if truncated {
		t.Errorf("small body reported truncated")
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}

	// Over the cap: exactly max bytes, truncated flagged.
	src := strings.Repeat("x", 500)
	body, truncated = readBodyCapped(strings.NewReader(src), 100)
	if !truncated {
		t.Errorf("oversized body not reported truncated")
	}
	if len(body) != 100 {
		t.Errorf("truncated body len = %d, want 100", len(body))
	}

	// Exactly at the cap is NOT truncated (max+1 read sees no overflow).
	body, truncated = readBodyCapped(strings.NewReader(strings.Repeat("y", 100)), 100)
	if truncated {
		t.Errorf("exact-cap body reported truncated")
	}
	if len(body) != 100 {
		t.Errorf("exact-cap body len = %d, want 100", len(body))
	}
}

func TestErrorBodySnippet(t *testing.T) {
	// Small body: trimmed, no marker.
	if got := errorBodySnippet(strings.NewReader("  boom  ")); got != "boom" {
		t.Errorf("snippet = %q, want %q", got, "boom")
	}

	// Oversized body: bounded and marked as truncated.
	huge := strings.Repeat("z", maxErrorBodyBytes*4)
	got := errorBodySnippet(strings.NewReader(huge))
	if !strings.HasSuffix(got, "[truncated]") {
		t.Errorf("oversized snippet missing truncation marker: ...%q", got[len(got)-20:])
	}
	// The snippet must stay bounded (cap + a short marker), never the full body.
	if len(got) > maxErrorBodyBytes+64 {
		t.Errorf("snippet len = %d, want <= %d", len(got), maxErrorBodyBytes+64)
	}
}

// TestDiscoverOpenAI_OversizedBodyControlledFailure: a 200 response whose body
// exceeds the discovery cap must fail cleanly (parse error on the truncated
// JSON) rather than allocate the whole body — the security property is a
// bounded read with a controlled failure, not a silent success.
func TestDiscoverOpenAI_OversizedBodyControlledFailure(t *testing.T) {
	// A valid JSON prefix followed by far more than the 1 MiB cap, so the
	// capped read yields invalid (truncated) JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[`))
		blob := strings.Repeat(`{"id":"x"},`, (maxDiscoveryBodyBytes/11)+1000)
		w.Write([]byte(blob))
	}))
	defer srv.Close()

	_, err := DiscoverOpenAI(context.Background(), "test-key", srv.URL)
	if err == nil {
		t.Fatalf("expected a controlled failure on an oversized discovery body, got nil")
	}
}
