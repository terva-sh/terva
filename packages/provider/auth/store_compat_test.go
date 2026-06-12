package auth

import (
	"path/filepath"
	"testing"
)

func TestStoreCompatWithKey(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetCompatAPIKey("openai-compatible", "sk-local", "http://localhost:1234/v1", "qwen2.5-coder", 131072); err != nil {
		t.Fatal(err)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !creds.Has("openai-compatible") {
		t.Fatal("Has=false after SetCompatAPIKey")
	}
	if got := creds.Method("openai-compatible"); got != "apikey" {
		t.Fatalf("method=%q", got)
	}
	bu, model, ctxWin := store.Extras("openai-compatible")
	if bu != "http://localhost:1234/v1" || model != "qwen2.5-coder" || ctxWin != 131072 {
		t.Fatalf("extras=(%q,%q,%d)", bu, model, ctxWin)
	}
}

// A keyless local endpoint (base URL only) must still persist and read
// back as a configured api-key login — many local servers need no token.
func TestStoreCompatKeyless(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetCompatAPIKey("openai-compatible", "", "http://localhost:8080/v1", "llama3", 0); err != nil {
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
	bu, model, _ := store.Extras("openai-compatible")
	if bu != "http://localhost:8080/v1" || model != "llama3" {
		t.Fatalf("extras=(%q,%q)", bu, model)
	}
}

// TestStoreCompatContextDefault confirms a keyless endpoint can still
// carry a default context window for discovered models.
func TestStoreCompatContextDefault(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetCompatAPIKey("openai-compatible", "", "http://localhost:8080/v1", "llama3", 8192); err != nil {
		t.Fatal(err)
	}
	if _, _, ctxWin := store.Extras("openai-compatible"); ctxWin != 8192 {
		t.Fatalf("context window=%d", ctxWin)
	}
}

func TestStoreCompatClear(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetCompatAPIKey("openai-compatible", "", "http://localhost:8080/v1", "llama3", 0); err != nil {
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
	if bu, model, ctxWin := store.Extras("openai-compatible"); bu != "" || model != "" || ctxWin != 0 {
		t.Fatalf("extras after clear=(%q,%q,%d)", bu, model, ctxWin)
	}
}
