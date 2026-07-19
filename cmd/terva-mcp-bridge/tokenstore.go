package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// oauthClient is the discovered authorization-server metadata plus the client
// registration for one MCP resource — everything a headless relay needs to
// refresh a token without re-running discovery. Persisted alongside the tokens.
type oauthClient struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ClientID              string   `json:"client_id"`
	ClientSecret          string   `json:"client_secret,omitempty"`
	Scopes                []string `json:"scopes,omitempty"`
	// Resource is the canonical MCP server URL, sent as the RFC 8707 resource
	// indicator so the issued token is audience-bound to this server.
	Resource string `json:"resource"`
}

// storedTokens is the persisted credential set for one resource. Expiry is
// absolute (computed from expires_in at fetch time) so a relay can tell a stale
// access token from a live one without a round-trip.
type storedTokens struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token,omitempty"`
	TokenType    string      `json:"token_type,omitempty"`
	Expiry       time.Time   `json:"expiry,omitempty"`
	Client       oauthClient `json:"client"`
}

// expired reports whether the access token is at or past its expiry, with a
// small skew so a token that dies mid-flight is refreshed proactively. A
// zero Expiry means "no known lifetime" — treated as live (the server will 401
// if it isn't, and the 401 path refreshes).
func (t storedTokens) expired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return !time.Now().Add(30 * time.Second).Before(t.Expiry)
}

// tokenStore persists one resource's tokens under
// $TERVA_HOME/mcp-bridge/<host-hash>/tokens.json at 0600. The directory is keyed
// by host plus a short hash of the full URL so two MCP servers on the same host
// (different paths) don't clobber each other's credentials.
type tokenStore struct{ path string }

func newTokenStore(baseDir, resourceURL string) (*tokenStore, error) {
	if baseDir == "" {
		baseDir = tervaHome()
	}
	if baseDir == "" {
		return nil, fmt.Errorf("cannot locate a data home for token storage; set TERVA_HOME or pass --token-dir")
	}
	key, err := resourceKey(resourceURL)
	if err != nil {
		return nil, err
	}
	return &tokenStore{path: filepath.Join(baseDir, "mcp-bridge", key, "tokens.json")}, nil
}

// resourceKey is the per-resource directory name: the sanitized host plus a
// short hash of the full URL, so the folder is human-recognizable yet unique per
// endpoint.
func resourceKey(resourceURL string) (string, error) {
	u, err := url.Parse(resourceURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("bridge: not a valid remote URL: %q", resourceURL)
	}
	host := strings.NewReplacer(":", "_", "/", "_").Replace(u.Host)
	sum := sha256.Sum256([]byte(resourceURL))
	return host + "-" + hex.EncodeToString(sum[:])[:8], nil
}

func (s *tokenStore) load() (storedTokens, bool, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return storedTokens{}, false, nil
	}
	if err != nil {
		return storedTokens{}, false, err
	}
	var t storedTokens
	if err := json.Unmarshal(b, &t); err != nil {
		return storedTokens{}, false, fmt.Errorf("bridge: token store at %s is corrupt: %w", s.path, err)
	}
	return t, t.AccessToken != "", nil
}

func (s *tokenStore) save(t storedTokens) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write never leaves a truncated token file,
	// and create the temp at 0600 so the secret is never briefly world-readable.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// tervaHome resolves terva's data home the same way the main binary does, so a
// bridge spawned by terva writes tokens where the user expects — without
// importing core. The explicit data-home env vars win; otherwise the OS-default
// terva dir. Kept deliberately small: this is a standalone tool.
func tervaHome() string {
	for _, env := range []string{"TERVA_HOME", "ZOT_HOME"} { // rename:keep — matches terva's Home() env compat
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "terva")
		}
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "terva")
		}
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "terva")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "terva")
	}
	return ""
}
