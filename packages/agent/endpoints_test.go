package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// RegisterEndpoint adds a dynamic provider; collisions with a built-in id and
// missing base URLs are rejected.
func TestRegisterEndpoint(t *testing.T) {
	if err := build.RegisterEndpoint("anthropic", config.EndpointConfig{BaseURL: "http://x/v1"}); err == nil {
		t.Error("an endpoint named like a built-in provider should be rejected")
	}
	if err := build.RegisterEndpoint("ep-missing-url", config.EndpointConfig{}); err == nil {
		t.Error("an endpoint without a base URL should be rejected")
	}
	t.Cleanup(func() { build.UnregisterEndpoint("ep-ok-unit") })
	if err := build.RegisterEndpoint("ep-ok-unit", config.EndpointConfig{BaseURL: "http://ep:9000/v1"}); err != nil {
		t.Fatalf("a fresh endpoint should register: %v", err)
	}
	if !build.IsKnownProvider("ep-ok-unit") {
		t.Error("endpoint not added to knownProviders")
	}
	// Re-registering the same id collides (idempotency is handled at the
	// RegisterEndpointsFromConfig level, not here).
	if err := build.RegisterEndpoint("ep-ok-unit", config.EndpointConfig{BaseURL: "http://ep:9000/v1"}); err == nil {
		t.Error("re-registering the same id should collide")
	}
}

// Resolve treats a registered endpoint like openai-compatible: its base URL
// flows through, and a keyless endpoint still resolves (sentinel bearer).
func TestResolveEndpointProvider(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"endpoints":{"box-resolve":{"baseUrl":"http://box-resolve:9000/v1"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { build.UnregisterEndpoint("box-resolve") })
	if err := build.RegisterEndpoint("box-resolve", config.EndpointConfig{BaseURL: "http://box-resolve:9000/v1"}); err != nil {
		t.Fatal(err)
	}

	r, err := build.Resolve(build.Args{Provider: "box-resolve", Model: "qwen-local"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "box-resolve" {
		t.Errorf("provider = %q, want box-resolve (not the unknown-provider fallback)", r.Provider)
	}
	if r.BaseURL != "http://box-resolve:9000/v1" {
		t.Errorf("base URL = %q, want the endpoint's URL", r.BaseURL)
	}
}

// EndpointNameFor derives a stable, collision-free endpoint id from a base URL.
func TestEndpointNameFor(t *testing.T) {
	used := map[string]bool{}
	cases := []struct{ url, want string }{
		{"http://box-a:8000/v1", "box-a"},
		{"http://localhost:1234/v1", "local-1234"},
		{"https://gw.internal/v1", "gw-internal"},
	}
	for _, c := range cases {
		if got := build.EndpointNameFor(c.url, used); got != c.want {
			t.Errorf("endpointNameFor(%q) = %q; want %q", c.url, got, c.want)
		}
	}
	// A second box-a dedupes.
	if got := build.EndpointNameFor("http://box-a:9999/v1", used); got != "box-a-2" {
		t.Errorf("dedup: got %q; want box-a-2", got)
	}
	// A name matching a built-in provider id is suffixed so it can't collide.
	if got := build.EndpointNameFor("http://anthropic/v1", map[string]bool{}); got != "anthropic-ep" {
		t.Errorf("built-in collision: got %q; want anthropic-ep", got)
	}
}

// mergeEndpointsIntoConfig adds new endpoints and never clobbers an existing one.
func TestMergeEndpointsIntoConfig(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"endpoints":{"box-a":{"baseUrl":"http://box-a:8000/v1"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := mergeEndpointsIntoConfig(map[string]config.EndpointConfig{
		"box-a": {BaseURL: "http://DIFFERENT/v1"},  // exists -> skipped, not clobbered
		"box-b": {BaseURL: "http://box-b:8000/v1"}, // new -> added
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d; want 1 (box-a skipped, box-b added)", added)
	}
	cfg, _ := config.LoadConfig()
	if cfg.Endpoints["box-a"].BaseURL != "http://box-a:8000/v1" {
		t.Errorf("existing box-a was clobbered: %q", cfg.Endpoints["box-a"].BaseURL)
	}
	if cfg.Endpoints["box-b"].BaseURL != "http://box-b:8000/v1" {
		t.Error("box-b should have been added")
	}
}

// endpointsFingerprint changes when the configured endpoint set changes, so a
// newly-added or edited endpoint forces a re-discovery instead of waiting out
// the cache TTL.
func TestEndpointsFingerprint(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{}`)
	if fp := endpointsFingerprint(); fp != "" {
		t.Errorf("no endpoints should give an empty fingerprint, got %q", fp)
	}

	write(`{"endpoints":{"a":{"baseUrl":"http://a/v1"},"b":{"baseUrl":"http://b/v1"}}}`)
	fp := endpointsFingerprint()
	if fp == "" {
		t.Fatal("expected a non-empty fingerprint with endpoints")
	}

	// Editing a base URL changes it.
	write(`{"endpoints":{"a":{"baseUrl":"http://a-CHANGED/v1"},"b":{"baseUrl":"http://b/v1"}}}`)
	if endpointsFingerprint() == fp {
		t.Error("changing an endpoint base URL should change the fingerprint")
	}
}
