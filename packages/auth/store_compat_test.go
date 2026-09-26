package auth

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestStoreCompatWithKey(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetCompatEndpoint("openai-compatible", "sk-local", CompatEndpoint{BaseURL: "http://localhost:1234/v1", Model: "qwen2.5-coder", ContextWindow: 131072}); err != nil {
		t.Fatal(err)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !creds.Has("openai-compatible") {
		t.Fatal("Has=false after SetCompatEndpoint")
	}
	if got := creds.Method("openai-compatible"); got != "apikey" {
		t.Fatalf("method=%q", got)
	}
	ep := store.CompatEndpointFor("openai-compatible")
	bu, model, ctxWin := ep.BaseURL, ep.Model, ep.ContextWindow
	if bu != "http://localhost:1234/v1" || model != "qwen2.5-coder" || ctxWin != 131072 {
		t.Fatalf("extras=(%q,%q,%d)", bu, model, ctxWin)
	}
}

// A keyless local endpoint (base URL only) must still persist and read
// back as a configured api-key login — many local servers need no token.
func TestStoreCompatKeyless(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetCompatEndpoint("openai-compatible", "", CompatEndpoint{BaseURL: "http://localhost:8080/v1", Model: "llama3", ContextWindow: 0}); err != nil {
		t.Fatal(err)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !creds.Has("openai-compatible") {
		t.Fatal("Has=false for keyless compat endpoint")
	}
	if got := creds.Method("openai-compatible"); got != "apikey" {
		t.Fatalf("method=%q", got)
	}
	ep := store.CompatEndpointFor("openai-compatible")
	bu, model := ep.BaseURL, ep.Model
	if bu != "http://localhost:8080/v1" || model != "llama3" {
		t.Fatalf("extras=(%q,%q)", bu, model)
	}
}

// TestStoreCompatContextDefault confirms a keyless endpoint can still
// carry a default context window for discovered models.
func TestStoreCompatContextDefault(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetCompatEndpoint("openai-compatible", "", CompatEndpoint{BaseURL: "http://localhost:8080/v1", Model: "llama3", ContextWindow: 8192}); err != nil {
		t.Fatal(err)
	}
	if ctxWin := store.CompatEndpointFor("openai-compatible").ContextWindow; ctxWin != 8192 {
		t.Fatalf("context window=%d", ctxWin)
	}
}

func TestStoreCompatClear(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetCompatEndpoint("openai-compatible", "", CompatEndpoint{BaseURL: "http://localhost:8080/v1", Model: "llama3", ContextWindow: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear("openai-compatible"); err != nil {
		t.Fatal(err)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if creds.Has("openai-compatible") {
		t.Fatal("Has=true after Clear")
	}
	if ep := store.CompatEndpointFor("openai-compatible"); ep.BaseURL != "" || ep.Model != "" || ep.ContextWindow != 0 {
		t.Fatalf("endpoint after clear=(%q,%q,%d)", ep.BaseURL, ep.Model, ep.ContextWindow)
	}
}
