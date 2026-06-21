package agent

import "testing"

// TestResolveInsecureGating pins the --insecure security boundary: it is
// allowed only for the openai-compatible / ollama providers with an
// explicit --base-url, and rejected everywhere else, so it can never
// silently weaken TLS verification for a built-in provider.
func TestResolveInsecureGating(t *testing.T) {
	// Allowed: openai-compatible with an explicit --base-url.
	r, err := Resolve(Args{Provider: "openai-compatible", BaseURL: "https://host:8443", Model: "x", Insecure: true}, false)
	if err != nil {
		t.Fatalf("openai-compatible + base-url + --insecure should resolve: %v", err)
	}
	if !r.Insecure {
		t.Fatal("Resolved.Insecure not set for a gated provider")
	}

	// Rejected: a built-in provider.
	if _, err := Resolve(Args{Provider: "anthropic", Insecure: true}, false); err == nil {
		t.Fatal("--insecure on anthropic should be rejected")
	}

	// Rejected: a gated provider but no explicit --base-url.
	if _, err := Resolve(Args{Provider: "ollama", Model: "m", Insecure: true}, false); err == nil {
		t.Fatal("--insecure without an explicit --base-url should be rejected")
	}

	// Sanity: without --insecure, the same gated provider resolves and is
	// secure.
	r2, err := Resolve(Args{Provider: "openai-compatible", BaseURL: "https://host:8443", Model: "x"}, false)
	if err != nil {
		t.Fatalf("openai-compatible should resolve without --insecure: %v", err)
	}
	if r2.Insecure {
		t.Fatal("Resolved.Insecure set without --insecure")
	}
}
