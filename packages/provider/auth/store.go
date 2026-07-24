// Package auth handles credential storage and the two login flows
// supported by terva: API key and (experimental) subscription OAuth.
//
// All credentials live in $TERVA_HOME/auth.json (mode 0600).
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/privfs"
)

// Credentials is the on-disk schema.
type Credentials struct {
	Anthropic             ProviderCreds            `json:"anthropic,omitempty"`
	OpenAI                ProviderCreds            `json:"openai,omitempty"`
	Kimi                  ProviderCreds            `json:"kimi,omitempty"`
	Google                ProviderCreds            `json:"google,omitempty"`
	DeepSeek              ProviderCreds            `json:"deepseek,omitempty"`
	GithubCopilot         ProviderCreds            `json:"github_copilot,omitempty"`
	AdditionalAPIKeyCreds map[string]ProviderCreds `json:"additional_api_key_creds,omitempty"`
}

// ProviderCreds holds credentials for a single provider. Most providers
// use either APIKey or OAuth; OpenAI may store both so the public API
// route and ChatGPT/Codex subscription route can coexist.
//
// BaseURL, Model and ContextWindow are only populated for the
// openai-compatible provider, whose endpoint has no catalog entry to
// fall back on. They're captured in the login form and persisted here:
// BaseURL is where requests go, Model is the default selection, and
// ContextWindow is the default size applied to discovered models the
// server doesn't describe (0 means "unknown / use the built-in default").
type ProviderCreds struct {
	APIKey        string      `json:"api_key,omitempty"`
	OAuth         *OAuthToken `json:"oauth,omitempty"`
	BaseURL       string      `json:"base_url,omitempty"`
	Model         string      `json:"model,omitempty"`
	ContextWindow int         `json:"context_window,omitempty"`
}

// OAuthToken is an OAuth 2 token set with refresh support.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	// ClientID that issued this token (informational).
	ClientID string `json:"client_id,omitempty"`
	// IDToken is the OIDC id_token (provider-specific; currently only
	// used by the OpenAI Codex flow to derive the ChatGPT account id).
	IDToken string `json:"id_token,omitempty"`
	// AccountID is the ChatGPT account id extracted from IDToken, used
	// as the `chatgpt-account-id` header when calling chatgpt.com/backend-api.
	AccountID string `json:"account_id,omitempty"`
}

// Expired reports whether the token has passed its expiry (with a 60s
// safety margin). Zero expiry is treated as non-expiring.
func (t *OAuthToken) Expired() bool {
	if t == nil || t.Expiry.IsZero() {
		return false
	}
	return time.Now().After(t.Expiry.Add(-60 * time.Second))
}

// Has reports whether at least one credential is present for provider.
// A configured openai-compatible endpoint (base URL set, key optional)
// counts as present even without an API key.
func (c *Credentials) Has(provider string) bool {
	p := c.get(provider)
	return p != nil && (p.APIKey != "" || p.OAuth != nil || p.BaseURL != "")
}

// Method returns "apikey", "oauth", or "" for the given provider.
func (c *Credentials) Method(provider string) string {
	p := c.get(provider)
	if p == nil {
		return ""
	}
	if p.APIKey != "" {
		return "apikey"
	}
	if p.OAuth != nil {
		return "oauth"
	}
	// A keyless openai-compatible endpoint is still an api-key style login.
	if p.BaseURL != "" {
		return "apikey"
	}
	return ""
}

func (c *Credentials) get(provider string) *ProviderCreds {
	switch provider {
	case "anthropic":
		return &c.Anthropic
	case "openai":
		return &c.OpenAI
	case "kimi":
		return &c.Kimi
	case "google":
		return &c.Google
	case "deepseek":
		return &c.DeepSeek
	case "github-copilot":
		return &c.GithubCopilot
	}
	if c.AdditionalAPIKeyCreds != nil {
		if p, ok := c.AdditionalAPIKeyCreds[provider]; ok {
			return &p
		}
	}
	return nil
}

func (c *Credentials) setAdditional(provider string, p ProviderCreds) {
	if c.AdditionalAPIKeyCreds == nil {
		c.AdditionalAPIKeyCreds = map[string]ProviderCreds{}
	}
	if p.APIKey == "" && p.OAuth == nil && p.BaseURL == "" {
		delete(c.AdditionalAPIKeyCreds, provider)
		if len(c.AdditionalAPIKeyCreds) == 0 {
			c.AdditionalAPIKeyCreds = nil
		}
		return
	}
	c.AdditionalAPIKeyCreds[provider] = p
}

// Store is a mutex-guarded read/write handle to the auth file.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store bound to path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load reads the current credentials. Returns a zero Credentials if the
// file does not exist.
func (s *Store) Load() (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return c, nil
}

// SetAPIKey replaces the API key for provider and saves to disk.
func (s *Store) SetAPIKey(provider, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		cur.APIKey = key
		cur.OAuth = nil
		c.setAdditional(provider, cur)
		return s.saveLocked(c)
	}
	p := c.get(provider)
	if p == nil {
		c.setAdditional(provider, ProviderCreds{APIKey: key})
		return s.saveLocked(c)
	}
	p.APIKey = key
	if provider != "openai" {
		p.OAuth = nil
	}
	return s.saveLocked(c)
}

// SetCompatAPIKey stores credentials for an OpenAI-compatible endpoint:
// the (optional) API key plus the user-supplied base URL and model id.
// Unlike SetAPIKey it also persists BaseURL/Model and tolerates an empty
// key, since many local servers (LM Studio, llama.cpp, vLLM) accept any
// or no bearer token.
func (s *Store) SetCompatAPIKey(provider, key, baseURL, model string, contextWindow int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	cur := ProviderCreds{}
	if existing := c.get(provider); existing != nil {
		cur = *existing
	}
	cur.APIKey = key
	cur.OAuth = nil
	cur.BaseURL = baseURL
	cur.Model = model
	cur.ContextWindow = contextWindow
	c.setAdditional(provider, cur)
	return s.saveLocked(c)
}

// Extras returns the persisted base URL, default model and default
// context window for provider. Only the openai-compatible provider
// populates these (captured in its login form); every other provider
// returns zero values.
func (s *Store) Extras(provider string) (baseURL, model string, contextWindow int) {
	c, err := s.Load()
	if err != nil {
		return "", "", 0
	}
	if p := c.get(provider); p != nil {
		return p.BaseURL, p.Model, p.ContextWindow
	}
	return "", "", 0
}

// SetOAuth replaces the OAuth token for provider and saves to disk.
func (s *Store) SetOAuth(provider string, tok OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		cur.APIKey = ""
		cur.OAuth = &tok
		c.setAdditional(provider, cur)
		return s.saveLocked(c)
	}
	p := c.get(provider)
	if p == nil {
		c.setAdditional(provider, ProviderCreds{OAuth: &tok})
		return s.saveLocked(c)
	}
	if provider != "openai" {
		p.APIKey = ""
	}
	p.OAuth = &tok
	return s.saveLocked(c)
}

// Clear removes all credentials for provider.
func (s *Store) Clear(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		c.setAdditional(provider, ProviderCreds{})
		return s.saveLocked(c)
	}
	p := c.get(provider)
	if p == nil {
		c.setAdditional(provider, ProviderCreds{})
		return s.saveLocked(c)
	}
	*p = ProviderCreds{}
	return s.saveLocked(c)
}

// ClearAPIKey removes only the API key for provider, preserving any OAuth token.
func (s *Store) ClearAPIKey(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		cur.APIKey = ""
		c.setAdditional(provider, cur)
		return s.saveLocked(c)
	}
	p := c.get(provider)
	if p == nil {
		return nil
	}
	p.APIKey = ""
	return s.saveLocked(c)
}

// ClearOAuth removes only the OAuth token for provider, preserving any API key.
func (s *Store) ClearOAuth(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		cur.OAuth = nil
		c.setAdditional(provider, cur)
		return s.saveLocked(c)
	}
	p := c.get(provider)
	if p == nil {
		return nil
	}
	p.OAuth = nil
	return s.saveLocked(c)
}

func (s *Store) saveLocked(c Credentials) error {
	// Owner-only: this directory holds auth.json (OAuth tokens / API keys). Under
	// a permissive umask a plain 0755 would leave the credential root traversable
	// by other local users; privfs pins 0700 regardless of umask.
	if err := privfs.MkdirAll(filepath.Dir(s.path)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically: write temp then rename.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
