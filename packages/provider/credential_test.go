package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStaticCredential(t *testing.T) {
	got, err := StaticCredential("sk-abc")(context.Background())
	if err != nil || got != "sk-abc" {
		t.Fatalf("StaticCredential = (%q, %v), want (sk-abc, nil)", got, err)
	}
}

// TestCredentialRotatesPerStreamWithoutRebuild is the regression test for the
// usage-window flicker: a client resolves its CredentialSource once per Stream,
// so a rotating OAuth token is picked up on the NEXT turn — and because the
// same client object serves both turns (no rebuild), the usage snapshot it
// parsed from the first response survives into the second. The old model
// rebuilt the client on every rotation and discarded that snapshot, blanking
// the 5h/weekly windows until the next response repopulated them.
func TestCredentialRotatesPerStreamWithoutRebuild(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("authorization"))
		mu.Unlock()
		// Codex records usage from response HEADERS (before the body is
		// parsed), so a header-only 200 is enough to populate the snapshot.
		w.Header().Set("x-codex-primary-used-percent", "42")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A source that hands out a fresh token each turn, as an OAuth refresh would.
	var n int
	rotating := func(context.Context) (string, error) {
		n++
		return fmt.Sprintf("tok%d", n), nil
	}
	c := NewOpenAICodexSource(rotating, "acct", srv.URL)

	drain := func() {
		evs, err := c.Stream(context.Background(), Request{Model: "gpt-5.5"})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for range evs { //nolint:revive // consume to completion
		}
	}

	drain()
	if _, ok := ClientUsage(c); !ok {
		t.Fatal("usage snapshot should populate from the first response's headers")
	}
	drain()
	// Same client object across both turns → the snapshot from turn 1 is still
	// there (never blanked by a rebuild), and the credential rotated.
	if _, ok := ClientUsage(c); !ok {
		t.Fatal("usage snapshot must survive a credential rotation (no rebuild)")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("want 2 requests, got %d", len(seen))
	}
	if seen[0] != "Bearer tok1" || seen[1] != "Bearer tok2" {
		t.Errorf("credential did not rotate per Stream: got %v, want [Bearer tok1 Bearer tok2]", seen)
	}
}
