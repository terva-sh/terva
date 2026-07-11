package auth

import (
	"strings"
	"testing"
)

func TestReadCappedBody(t *testing.T) {
	// Under the cap: full body, not truncated.
	body, truncated := readCappedBody(strings.NewReader("token-response"), 64)
	if truncated {
		t.Errorf("small body reported truncated")
	}
	if string(body) != "token-response" {
		t.Errorf("body = %q, want %q", body, "token-response")
	}

	// Over the cap: exactly max bytes, truncated flagged (bounds a hostile
	// token endpoint that streams an unbounded response).
	body, truncated = readCappedBody(strings.NewReader(strings.Repeat("x", 4096)), 512)
	if !truncated {
		t.Errorf("oversized body not reported truncated")
	}
	if len(body) != 512 {
		t.Errorf("truncated body len = %d, want 512", len(body))
	}
}
