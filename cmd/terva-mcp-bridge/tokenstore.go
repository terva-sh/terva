package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/secrets"
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
//
// The file is encrypted WHOLE when terva's at-rest key is configured, the same
// shape auth.json uses and for the same reason: nothing but this bridge reads
// it, so there is no structure worth leaving legible, and hiding which servers
// you have authenticated to is free. Per-value sealing is for files a SECOND
// party also owns — see docs/proposals/secrets-at-rest.md §8.3.
//
// home is kept so the key can be resolved at each load and save rather than
// cached: a cached identity outlives a rotation and would seal a later save to
// the retired key.
type tokenStore struct {
	path string
	home string
}

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
	return &tokenStore{
		path: filepath.Join(baseDir, "mcp-bridge", key, "tokens.json"),
		home: baseDir,
	}, nil
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
	// Sniff rather than remember: the file may predate `terva secret init`, and
	// a store that decides by configuration instead of by content would refuse
	// a plaintext file the moment a key appears.
	if secrets.IsAgeFile(b) {
		id, keyErr := secrets.IdentityIn(s.home)
		if keyErr != nil {
			// Fail CLOSED and loudly. Falling through to "no tokens" would send
			// the bridge to re-authenticate, which looks like an expired
			// session and quietly abandons working credentials.
			return storedTokens{}, false, fmt.Errorf("bridge: token store at %s is encrypted and the key is unavailable: %w", s.path, keyErr)
		}
		b, err = secrets.Decrypt(id, b)
		if err != nil {
			return storedTokens{}, false, fmt.Errorf("bridge: token store at %s could not be decrypted: %w", s.path, err)
		}
	}
	var t storedTokens
	if err := json.Unmarshal(b, &t); err != nil {
		return storedTokens{}, false, fmt.Errorf("bridge: token store at %s is corrupt: %w", s.path, err)
	}
	return t, t.AccessToken != "", nil
}

func (s *tokenStore) save(t storedTokens) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Seal when a key is configured. ErrNoKey means encryption is simply not
	// set up and plaintext is correct, as before; any OTHER resolver error is a
	// present-but-broken key and must fail the write rather than silently
	// downgrade it to plaintext.
	id, keyErr := secrets.IdentityIn(s.home)
	switch {
	case keyErr == nil:
		if b, err = secrets.Encrypt(b, id.Recipient()); err != nil {
			return fmt.Errorf("bridge: encrypting the token store: %w", err)
		}
	// keyErr != nil is stated rather than implied: errors.Is(nil, ErrNoKey) is
	// false, so without it this arm would fire on a nil error if the arms were
	// ever reordered, and wrap it into a message about a broken key.
	case keyErr != nil && !errors.Is(keyErr, secrets.ErrNoKey):
		return fmt.Errorf("bridge: a secrets key is configured but unusable, refusing to write tokens in the clear: %w", keyErr)
	}
	// Write-then-rename so a crash mid-write never leaves a truncated token
	// file. A UNIQUE temp, not "<path>.tmp": two bridges refreshing the same
	// resource at once would otherwise write the same scratch path and one
	// would rename the other's half-written bytes into place.
	f, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, s.path)
}

// renameAttempts / renameBackoff bound the Windows retry below. Eight
// concurrent savers is the documented contention case; a handful of
// millisecond-scale retries covers it without turning a genuine permission
// error into a visible stall.
const (
	renameAttempts = 8
	renameBackoff  = 2 * time.Millisecond
)

// replaceFile moves src onto dst, which must already be written and closed.
//
// os.Rename is atomic on POSIX and always wins a race with a concurrent
// replace. On Windows it is MoveFileEx, which reports ERROR_ACCESS_DENIED
// while another writer briefly holds the destination open — so two bridges
// refreshing the same resource would lose a save that POSIX keeps. Retry only
// that class; every other error is returned as it arrives, so a genuinely
// unwritable path still fails fast. On POSIX the first attempt succeeds and
// the loop costs nothing.
func replaceFile(src, dst string) error {
	var err error
	for attempt := 0; attempt < renameAttempts; attempt++ {
		err = os.Rename(src, dst)
		if err == nil || !errors.Is(err, fs.ErrPermission) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * renameBackoff)
	}
	return err
}

// tervaHome resolves terva's data home by CALLING the resolver terva itself
// calls, rather than reproducing it.
//
// This used to be a hand-written copy, documented as resolving "the same way the
// main binary does". It did not: envcompat.Home has four steps and the copy had
// two, dropping both exists-based discovery arms. On a pre-rename install with
// no TERVA_HOME set, terva resolves to the legacy data dir and the copy resolved
// to the current one — so the bridge looked for secrets.key in a directory that
// has none, took the "encryption is not set up" branch, and wrote the OAuth
// access token, refresh token and client_secret in CLEARTEXT while
// `terva secret status` reported at-rest encryption as ON.
//
// envcompat is a leaf package (stdlib plus privfs) written to be shared by
// exactly this kind of consumer, and connsdk — the other standalone-binary SDK —
// already resolves its state dir through it.
func tervaHome() string { return envcompat.Home() }
