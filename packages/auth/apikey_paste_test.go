package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The api-key form server binds to loopback on a random port, so on a
// headless host it cannot be reached. CompleteAPIKey and CompleteCompatAPIKey
// are the way in, and these tests pin their contract.

// newPasteManager returns a Manager whose probes always pass, so the tests
// exercise validation and storage without touching the network.
func newPasteManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(NewStore(filepath.Join(testsupport.TempDir(t), "auth.json")))
	m.probeAPIKey = func(context.Context, string, string) error { return nil }
	m.probeCompat = func(context.Context, string, string) error { return nil }
	return m
}

// storedKey reads a key back through the same generic path the rest of the
// codebase uses, so providers held in AdditionalAPIKeyCreds work too.
func storedKey(t *testing.T, m *Manager, provider string) string {
	t.Helper()
	c, err := m.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pc := c.get(provider)
	if pc == nil {
		return ""
	}
	return pc.APIKey
}

// drainEvents collects the events emitted while fn runs.
func drainEvents(t *testing.T, m *Manager, fn func()) []Event {
	t.Helper()
	fn()
	var out []Event
	for {
		select {
		case ev := <-m.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestCompleteAPIKeyStoresAndEmitsSuccess(t *testing.T) {
	m := newPasteManager(t)

	evs := drainEvents(t, m, func() {
		if err := m.CompleteAPIKey(context.Background(), "opencode-go", "sk-test-key"); err != nil {
			t.Fatalf("CompleteAPIKey: %v", err)
		}
	})

	if got := storedKey(t, m, "opencode-go"); got != "sk-test-key" {
		t.Errorf("stored key = %q, want %q", got, "sk-test-key")
	}
	if len(evs) != 1 || evs[0].Kind != "success" || evs[0].Method != "apikey" {
		t.Errorf("events = %+v, want one apikey success", evs)
	}
}

func TestCompleteAPIKeyTrimsWhitespace(t *testing.T) {
	m := newPasteManager(t)
	// A key pasted over SSH very often arrives with a trailing newline.
	if err := m.CompleteAPIKey(context.Background(), "opencode-go", "  sk-padded\n"); err != nil {
		t.Fatalf("CompleteAPIKey: %v", err)
	}
	if got := storedKey(t, m, "opencode-go"); got != "sk-padded" {
		t.Errorf("stored key = %q, want it trimmed", got)
	}
}

func TestCompleteAPIKeyRejectsEmptyAndEmits(t *testing.T) {
	m := newPasteManager(t)

	evs := drainEvents(t, m, func() {
		if err := m.CompleteAPIKey(context.Background(), "opencode-go", "   "); err == nil {
			t.Fatal("CompleteAPIKey(empty) = nil, want error")
		}
	})

	// The original bug was a silently-discarded error. Every failure path
	// must emit, or the TUI shows the user nothing at all.
	if len(evs) != 1 || evs[0].Kind != "error" {
		t.Fatalf("events = %+v, want one error event", evs)
	}
	if storedKey(t, m, "opencode-go") != "" {
		t.Error("an empty key was stored")
	}
}

func TestCompleteAPIKeyRejectsUnknownProvider(t *testing.T) {
	m := newPasteManager(t)
	evs := drainEvents(t, m, func() {
		if err := m.CompleteAPIKey(context.Background(), "not-a-provider", "sk-x"); err == nil {
			t.Fatal("CompleteAPIKey(unknown) = nil, want error")
		}
	})
	if len(evs) != 1 || evs[0].Kind != "error" {
		t.Fatalf("events = %+v, want one error event", evs)
	}
}

// Each compatible slot carries a base URL and a model id as well as a key, so
// a bare paste cannot complete it. It must fail loudly rather than store a
// key against an endpoint terva has no address for.
func TestCompleteAPIKeyRefusesTheCompatibleSlots(t *testing.T) {
	for _, p := range []string{compatProvider, anthropicCompatProvider} {
		t.Run(p, func(t *testing.T) {
			m := newPasteManager(t)

			evs := drainEvents(t, m, func() {
				err := m.CompleteAPIKey(context.Background(), p, "sk-x")
				if err == nil {
					t.Fatalf("CompleteAPIKey(%s) = nil, want error", p)
				}
				if !strings.Contains(err.Error(), "base url") {
					t.Errorf("error = %q, want it to name the missing base url", err)
				}
			})
			if len(evs) != 1 || evs[0].Kind != "error" {
				t.Fatalf("events = %+v, want one error event", evs)
			}
		})
	}
}

// Every api-key provider the login dialog can reach must be completable
// from the TUI, since that is the only headless path. The two compatible
// slots are the documented exceptions, and they share their own entry point.
func TestCompleteAPIKeyCoversEveryAPIKeyProvider(t *testing.T) {
	for _, p := range APIKeyProviders() {
		if isCompatProvider(p) {
			continue
		}
		m := newPasteManager(t)
		if err := m.CompleteAPIKey(context.Background(), p, "sk-test"); err != nil {
			t.Errorf("CompleteAPIKey(%q) = %v, want it to succeed", p, err)
			continue
		}
		if got := storedKey(t, m, p); got != "sk-test" {
			t.Errorf("CompleteAPIKey(%q): stored %q, want it persisted", p, got)
		}
	}
}

// The browser form probes a key before saving it. The headless path must do
// the same, or a typo is stored silently and only fails on the first real
// request — a much more confusing failure, far from its cause.
func TestCompleteAPIKeyProbesBeforeStoring(t *testing.T) {
	m := newPasteManager(t)
	m.probeAPIKey = func(context.Context, string, string) error {
		return errors.New("401 unauthorized")
	}

	evs := drainEvents(t, m, func() {
		if err := m.CompleteAPIKey(context.Background(), "opencode-go", "sk-wrong"); err == nil {
			t.Fatal("CompleteAPIKey with a failing probe = nil, want error")
		}
	})

	if len(evs) != 1 || evs[0].Kind != "error" {
		t.Fatalf("events = %+v, want one error event", evs)
	}
	if storedKey(t, m, "opencode-go") != "" {
		t.Error("a key that failed its probe was stored anyway")
	}
}

// ---- openai-compatible ----

func TestCompleteCompatAPIKeyStoresEveryField(t *testing.T) {
	m := newPasteManager(t)

	evs := drainEvents(t, m, func() {
		err := m.CompleteCompatAPIKey(context.Background(), compatProvider,
			CompatEndpoint{BaseURL: "http://localhost:1234/v1", Model: "qwen2.5-coder", ContextWindow: 32768}, "sk-local")
		if err != nil {
			t.Fatalf("CompleteCompatAPIKey: %v", err)
		}
	})

	if len(evs) != 1 || evs[0].Kind != "success" {
		t.Fatalf("events = %+v, want one success event", evs)
	}
	ep := m.store.CompatEndpointFor("openai-compatible")
	baseURL, model, window := ep.BaseURL, ep.Model, ep.ContextWindow
	if baseURL != "http://localhost:1234/v1" {
		t.Errorf("base url = %q", baseURL)
	}
	if model != "qwen2.5-coder" {
		t.Errorf("model = %q", model)
	}
	if window != 32768 {
		t.Errorf("context window = %d, want 32768", window)
	}
	if got := storedKey(t, m, "openai-compatible"); got != "sk-local" {
		t.Errorf("key = %q", got)
	}
}

// Local servers (lm studio, llama.cpp, ollama) routinely ignore the key, and
// the browser form treats it as optional. The TUI form must agree.
func TestCompleteCompatAPIKeyAllowsAnEmptyKey(t *testing.T) {
	m := newPasteManager(t)
	err := m.CompleteCompatAPIKey(context.Background(), compatProvider,
		CompatEndpoint{BaseURL: "http://localhost:11434/v1", Model: "llama3", ContextWindow: 0}, "")
	if err != nil {
		t.Fatalf("CompleteCompatAPIKey with no key = %v, want success", err)
	}
	ep := m.store.CompatEndpointFor("openai-compatible")
	baseURL, model := ep.BaseURL, ep.Model
	if baseURL == "" || model == "" {
		t.Errorf("endpoint not stored: base=%q model=%q", baseURL, model)
	}
}

func TestCompleteCompatAPIKeyRequiresBaseURLAndModel(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, model string
	}{
		{"no base url", "", "llama3"},
		{"no model", "http://localhost:11434/v1", ""},
		{"neither", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPasteManager(t)
			evs := drainEvents(t, m, func() {
				if err := m.CompleteCompatAPIKey(context.Background(), compatProvider,
					CompatEndpoint{BaseURL: tc.baseURL, Model: tc.model}, "k"); err == nil {
					t.Fatal("want an error")
				}
			})
			if len(evs) != 1 || evs[0].Kind != "error" {
				t.Fatalf("events = %+v, want one error event", evs)
			}
		})
	}
}

func TestCompleteCompatAPIKeyProbesBeforeStoring(t *testing.T) {
	m := newPasteManager(t)
	m.probeCompat = func(context.Context, string, string) error {
		return errors.New("connection refused")
	}
	err := m.CompleteCompatAPIKey(context.Background(), compatProvider,
		CompatEndpoint{BaseURL: "http://localhost:9/v1", Model: "nope", ContextWindow: 0}, "")
	if err == nil {
		t.Fatal("CompleteCompatAPIKey with a failing probe = nil, want error")
	}
	if ep := m.store.CompatEndpointFor("openai-compatible"); ep.BaseURL != "" {
		t.Error("an unreachable endpoint was stored anyway")
	}
}
