package build

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// An expired token with no refresh_token fails refresh WITHOUT a network call
// (RefreshIfExpired short-circuits), which is what keeps these tests hermetic.
func deadToken() auth.OAuthToken {
	return auth.OAuthToken{
		AccessToken: "expired-access-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-24 * time.Hour),
	}
}

func liveToken(access string) auth.OAuthToken {
	return auth.OAuthToken{
		AccessToken: access,
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(24 * time.Hour),
	}
}

// isolate points TERVA_HOME and the OS home dir at fresh temp dirs and clears
// every provider env var that could pre-empt the credential under test.
func isolate(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	for _, k := range []string{
		"KIMI_API_KEY", "MOONSHOT_API_KEY", "ANTHROPIC_API_KEY",
		"ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	return home
}

// writeKimiCLIToken plants a token where the official Kimi Code CLI keeps one.
func writeKimiCLIToken(t *testing.T, home string, tok auth.OAuthToken) {
	t.Helper()
	dir := filepath.Join(home, ".kimi", "credentials")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
		"token_type":    tok.TokenType,
		"expires_at":    float64(tok.Expiry.Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kimi-code.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A subscription whose access token has expired and whose refresh was refused
// is NOT a usable credential. Handing the dead token back — which is what
// discarding RefreshIfExpired's error did — bought one thing: a 401 from the
// provider, several layers from the login that lapsed, wearing the wire
// client's name instead of its own.
func TestResolveCredentialRefusesExpiredKimiSubscription(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetOAuth("kimi", deadToken()); err != nil {
		t.Fatal(err)
	}

	cred, _, _, err := ResolveCredentialFull("kimi", "")
	if err == nil {
		t.Fatalf("resolved a dead subscription as a usable credential (%q)", cred)
	}
	if cred != "" {
		t.Errorf("credential = %q, want empty — a dead token must not reach a request", cred)
	}
	var expired *ExpiredLoginError
	if !errors.As(err, &expired) {
		t.Fatalf("error is not an *ExpiredLoginError: %v", err)
	}
	if expired.Provider != "kimi" {
		t.Errorf("ExpiredLoginError.Provider = %q, want kimi", expired.Provider)
	}
	if !strings.Contains(err.Error(), "/login") {
		t.Errorf("message does not name the remedy: %v", err)
	}
}

// The CLI fallback is why terva can run kimi with no login of its own. A dead
// STORED token used to step in front of it and return itself, so a lapsed terva
// login broke kimi on a machine where the official CLI was signed in and fine.
func TestResolveCredentialFallsBackPastExpiredKimiSubscription(t *testing.T) {
	home := isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(false); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetOAuth("kimi", deadToken()); err != nil {
		t.Fatal(err)
	}
	writeKimiCLIToken(t, home, liveToken("cli-access-token"))

	cred, method, _, err := ResolveCredentialFull("kimi", "")
	if err != nil {
		t.Fatalf("ResolveCredentialFull: %v", err)
	}
	if cred != "cli-access-token" {
		t.Errorf("credential = %q, want the CLI's live token", cred)
	}
	if method != "oauth" {
		t.Errorf("method = %q, want oauth", method)
	}
}

// Same rule for the other two subscription routes, so the next one added
// inherits a shape that is already right rather than the one that was wrong.
func TestResolveCredentialRefusesExpiredSubscriptions(t *testing.T) {
	for _, tc := range []struct {
		provider string
		store    string // auth.json key, which is not always the provider id
	}{
		{"anthropic", "anthropic"},
		{"openai-codex", "openai"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			isolate(t)
			if err := config.AuthStoreFor().SetOAuth(tc.store, deadToken()); err != nil {
				t.Fatal(err)
			}
			cred, _, _, err := ResolveCredentialFull(tc.provider, "")
			if err == nil {
				t.Fatalf("resolved a dead subscription as usable (%q)", cred)
			}
			if cred != "" {
				t.Errorf("credential = %q, want empty", cred)
			}
			var expired *ExpiredLoginError
			if !errors.As(err, &expired) {
				t.Fatalf("error is not an *ExpiredLoginError: %v", err)
			}
			if expired.Provider != tc.provider {
				t.Errorf("ExpiredLoginError.Provider = %q, want %q", expired.Provider, tc.provider)
			}
		})
	}
}

// Resolve must pass the lapsed-login message through instead of flattening it
// into noCredentialError's "set KIMI_API_KEY" — an errand for an account that
// was never configured, told to someone whose credential is on disk and whose
// grant merely expired.
func TestResolveReportsLapsedLoginNotMissingCredential(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetOAuth("kimi", deadToken()); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(Args{Provider: "kimi", Model: "k3"}, true)
	if err == nil {
		t.Fatal("Resolve succeeded with a dead kimi subscription")
	}
	msg := err.Error()
	if !strings.Contains(msg, "expired") || !strings.Contains(msg, "/login") {
		t.Errorf("message does not describe a lapsed login: %v", err)
	}
	if strings.Contains(msg, "KIMI_API_KEY") {
		t.Errorf("message sends the user after an API key that was never the mechanism: %v", err)
	}
	var credErr *CredentialError
	if !errors.As(err, &credErr) {
		t.Errorf("error is not a *CredentialError: %v", err)
	}
}

// A built-in catalog row's BaseURL is the VENDOR's hosted api, not an
// invitation to skip authentication. Resolve used to read any base URL as
// "local model, no key needed" and hand the client the literal string "ollama"
// — so every hosted provider with a BaseURL (kimi, deepseek, fireworks,
// minimax, groq, openrouter, ...) turned a missing login into a 401 from the
// vendor instead of a refusal at resolution time.
func TestResolveRequiresCredentialForHostedCatalogModel(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Provider: "kimi", Model: "k3"}, false)
	if err != nil {
		t.Fatalf("Resolve(requireCred=false): %v", err)
	}
	if r.Credential == "ollama" {
		t.Fatal(`resolved the sentinel "ollama" as kimi's API key`)
	}
	if r.HasCredential() {
		t.Fatalf("credential = %q, want none for an unauthenticated hosted provider", r.Credential)
	}
	if r.CredentialErr == nil {
		t.Error("CredentialErr is nil; the host has nothing to report")
	}

	if _, err := Resolve(Args{Provider: "kimi", Model: "k3"}, true); err == nil {
		t.Error("Resolve(requireCred=true) succeeded with no kimi credential")
	}
}

// The other half of that rule: a models.json entry pinning a server the user
// runs is exactly the keyless case, and must keep booting without a credential.
// Any entry models.json touches is stamped Source "user", including an override
// that only re-points a built-in row's baseUrl.
func TestResolveKeepsUserPinnedModelKeyless(t *testing.T) {
	isolate(t)
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider:      "openai",
		ID:            "local-qwen",
		DisplayName:   "Local Qwen",
		ContextWindow: 32768,
		MaxOutput:     8192,
		BaseURL:       "http://localhost:8000/v1",
		Source:        "user",
	}})

	r, err := Resolve(Args{Provider: "openai", Model: "local-qwen"}, true)
	if err != nil {
		t.Fatalf("a user-pinned local model must resolve without a credential: %v", err)
	}
	if !r.HasCredential() {
		t.Error("no sentinel credential was substituted for the keyless endpoint")
	}
	if r.BaseURL != "http://localhost:8000/v1" {
		t.Errorf("BaseURL = %q, want the pinned endpoint", r.BaseURL)
	}
}
