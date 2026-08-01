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
	"sort"
	"sync"
	"time"

	"terva.sh/terva/packages/filelock"
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
	// IssuedAt is when the provider minted this token. It exists so the
	// refresh window can be a FRACTION of the token's life rather than a
	// fixed margin: an 8-hour anthropic token and a 10-day openai one do not
	// want the same lead time. Additive and omitempty — a token stored before
	// this field existed has a zero value, and StaleFor falls back to a fixed
	// margin for it rather than guessing a lifetime.
	IssuedAt time.Time `json:"issued_at,omitempty"`
}

// Expired reports whether the token has passed its expiry (with a 60s
// safety margin). Zero expiry is treated as non-expiring.
//
// This is the "can this token still authenticate a request" question. For
// "should we refresh it yet", which fires much earlier, see NeedsRefresh.
func (t *OAuthToken) Expired() bool {
	if t == nil || t.Expiry.IsZero() {
		return false
	}
	return time.Now().After(t.Expiry.Add(-60 * time.Second))
}

// Refresh-window tuning.
//
// Measured lifetimes, because the first cut of this was sized by extrapolating
// from two providers and got the third wrong by a factor of thirty:
//
//	kimi       15 minutes   (Kimi Code subscription, measured 2026-07-30)
//	anthropic   8 hours
//	openai     10 days
//
// The FRACTION is the rule; the floor is a minimum retry margin for tokens long
// enough to afford one. Anything that lets the floor outgrow the token is a bug —
// a flat 10-minute floor on kimi's 15-minute token leaves five minutes of usable
// life and comes due on essentially every sweep.
const (
	refreshFraction = 0.20
	refreshFloor    = 10 * time.Minute
	// refreshFloorShare caps the floor's reach into a short token: the window
	// may never exceed this share of the whole lifetime.
	refreshFloorShare = 3
	// refreshFallback is the window when IssuedAt is absent, which means the
	// lifetime is unknown and no fraction can be taken of it.
	//
	// Thin, because a wide guess is what made a pre-IssuedAt kimi token due at
	// the instant it was minted — but not thinner than one sweep can be late, or
	// such a token expires without any sweep having looked at it. That lower
	// bound is asserted against the tick in authrefresh, which is the only place
	// both numbers are visible at once; a first draft of 2 minutes was caught
	// there, being shorter than the tick's own worst case.
	//
	// A token whose whole life is under this would still be born stale. That
	// costs ONE immediate refresh, not a loop: the refresh stamps IssuedAt, and
	// every evaluation after it takes the proportional path above.
	refreshFallback = 5 * time.Minute
)

// StaleFor reports how long the token has been due for refresh, and whether it
// is due at all. A positive duration is how far past the refresh point we are —
// callers use it to order several stale providers worst-first.
//
// The window is refreshFraction × lifetime, raised to refreshFloor only as far
// as a third of the lifetime allows. That margin is the whole point: it is what
// a failed attempt has left to retry in, and why a laptop that sleeps through a
// tick has not lost the grant.
//
// The floor is capped rather than absolute because a token can be shorter than
// it. Kimi mints FIFTEEN-MINUTE access tokens; an uncapped 10-minute floor left
// five minutes of usable life and came due on every sweep, and the same shape
// with IssuedAt absent made the token due the instant it was minted — a refresh
// loop, not a policy.
//
// Without IssuedAt there is no lifetime to take a fraction of, so the token gets
// refreshFallback. See its comment for why that is deliberately thin.
func (t *OAuthToken) StaleFor(now time.Time) (time.Duration, bool) {
	if t == nil || t.Expiry.IsZero() {
		return 0, false
	}
	window := refreshFallback
	if !t.IssuedAt.IsZero() {
		if lifetime := t.Expiry.Sub(t.IssuedAt); lifetime > 0 {
			window = time.Duration(float64(lifetime) * refreshFraction)
			if window < refreshFloor {
				// min, not the floor outright: the floor is a margin for tokens
				// that can afford one, never a claim on most of a short token.
				window = min(refreshFloor, lifetime/refreshFloorShare)
			}
		}
	}
	due := t.Expiry.Add(-window)
	if now.Before(due) {
		return 0, false
	}
	return now.Sub(due), true
}

// NeedsRefresh reports whether the token has entered its refresh window.
func (t *OAuthToken) NeedsRefresh() bool {
	_, due := t.StaleFor(time.Now())
	return due
}

// OAuthProviders names every provider holding an OAuth token, in a stable
// order. It is how a proactive refresh finds the credentials nothing is
// currently USING: boot resolves only the selected provider, so a subscription
// the user is not talking to right now would otherwise sit untouched until its
// refresh window had come and gone.
//
// Names are auth.json's keys, which are not always terva provider ids — codex
// tokens live under "openai". That is the spelling the refresh flows want.
func (c *Credentials) OAuthProviders() []string {
	var out []string
	for _, name := range []string{"anthropic", "openai", "kimi", "google", "deepseek", "github-copilot"} {
		if p := c.get(name); p != nil && p.OAuth != nil && p.OAuth.AccessToken != "" {
			out = append(out, name)
		}
	}
	names := make([]string, 0, len(c.AdditionalAPIKeyCreds))
	for name := range c.AdditionalAPIKeyCreds {
		names = append(names, name)
	}
	sort.Strings(names) // map order is not an order
	for _, name := range names {
		if p := c.AdditionalAPIKeyCreds[name]; p.OAuth != nil && p.OAuth.AccessToken != "" {
			out = append(out, name)
		}
	}
	return out
}

// OAuthFor returns provider's stored OAuth token, or nil.
//
// A read-only accessor: the pointer behind it is a copy for the additional-creds
// case, so writing through it persists nothing. Writes go through Mutate.
func (c *Credentials) OAuthFor(provider string) *OAuthToken {
	p := c.get(provider)
	if p == nil {
		return nil
	}
	return p.OAuth
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

// Store is a read/write handle to the auth file, serialized both ways it can
// be contended.
//
// Every write here is a whole-file read-modify-write: load all of Credentials,
// change one provider's entry, write all of it back. That shape makes two
// concurrent writers lose one update — A loads, B loads, A writes {A′,B},
// B writes {A,B′} — and the atomic rename that keeps a reader from ever seeing
// a torn file is precisely what stops anyone noticing.
//
// mu orders writers inside one process. It only does that for callers holding
// the SAME Store, which is why config.AuthStoreFor hands out a shared instance
// per path rather than minting one per call.
//
// lockPath orders writers across processes, which mu cannot: several terva
// instances share one $TERVA_HOME, and refreshing an OAuth token is a write
// each of them makes on its own schedule. See packages/filelock.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store bound to path.
//
// Prefer config.AuthStoreFor: two Stores on one path have two mutexes and
// therefore order nothing between them.
func NewStore(path string) *Store { return &Store{path: path} }

// lockPath is the cross-process lock guarding this file. It sits beside
// auth.json rather than inside it — a lock is not a credential, and a
// crash must not leave the file it guards holding a stale claim.
func (s *Store) lockPath() string { return s.path + ".lock" }

// Mutate applies fn to the stored credentials and writes the result, holding
// both locks for the whole read-modify-write.
//
// This is the ONLY way to write this file. Load-then-Set from a caller is the
// lost update above with extra steps: the window between the two is exactly
// where another process's write goes missing.
func (s *Store) Mutate(fn func(*Credentials)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(fn)
}

// mutateLocked is Mutate with s.mu already held.
func (s *Store) mutateLocked(fn func(*Credentials)) error {
	// The lock is taken AFTER s.mu, always, and released before it. One
	// ordering everywhere is what keeps two Stores in one process from
	// deadlocking against each other.
	lk, err := filelock.Acquire(s.lockPath())
	if err != nil {
		// A home that cannot host a lockfile (read-only mount, exotic
		// filesystem) must not become a home where logging in fails. Degrade to
		// the in-process mutex, which is what this had before the lock existed.
		lk = nil
	}
	defer lk.Release()
	// Load INSIDE the lock. Anything read before acquiring it describes the
	// file as it was before the writer we just queued behind.
	c, err := s.loadLocked()
	if err != nil {
		return err
	}
	fn(&c)
	return s.saveLocked(c)
}

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
	return s.Mutate(func(c *Credentials) {
		if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
			cur.APIKey = key
			cur.OAuth = nil
			c.setAdditional(provider, cur)
			return
		}
		p := c.get(provider)
		if p == nil {
			c.setAdditional(provider, ProviderCreds{APIKey: key})
			return
		}
		p.APIKey = key
		if provider != "openai" {
			p.OAuth = nil
		}
	})
}

// SetCompatAPIKey stores credentials for an OpenAI-compatible endpoint:
// the (optional) API key plus the user-supplied base URL and model id.
// Unlike SetAPIKey it also persists BaseURL/Model and tolerates an empty
// key, since many local servers (LM Studio, llama.cpp, vLLM) accept any
// or no bearer token.
func (s *Store) SetCompatAPIKey(provider, key, baseURL, model string, contextWindow int) error {
	return s.Mutate(func(c *Credentials) {
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
	})
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
	return s.Mutate(func(c *Credentials) { setOAuthIn(c, provider, tok) })
}

// setOAuthIn writes tok into c. Split out of SetOAuth so RefreshOAuth, which
// holds the lock across a network call, stores its result the same way rather
// than growing a second copy of these three branches.
func setOAuthIn(c *Credentials, provider string, tok OAuthToken) {
	if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
		cur.APIKey = ""
		cur.OAuth = &tok
		c.setAdditional(provider, cur)
		return
	}
	p := c.get(provider)
	if p == nil {
		c.setAdditional(provider, ProviderCreds{OAuth: &tok})
		return
	}
	if provider != "openai" {
		p.APIKey = ""
	}
	p.OAuth = &tok
}

// RefreshOAuth mints a replacement for provider's stored token with the
// cross-process lock held, and persists it.
//
// The re-read after acquiring the lock is the whole design. N instances that
// wake together — started by one shell script, or ticking on the same
// schedule — all queue on the lock; the winner refreshes, and every loser
// then finds a token on disk that no longer needs refreshing and returns it
// without touching the network. One refresh call, not N.
//
// That matters beyond politeness to the auth server. A provider that ROTATES
// refresh tokens (OAuth 2.1 and RFC 9700 both recommend it) treats a replayed
// refresh token as an attack and revokes the whole grant — so N concurrent
// refreshes is not N times the work, it is a logged-out user. The double-check
// is what stops a loser presenting a token the winner already spent.
//
// mint receives the CURRENT stored token, not whatever the caller was holding:
// a caller that loaded before queueing on the lock has a stale copy by
// definition. A mint failure leaves the stored token untouched and returns it
// alongside the error — it may still have hours of life, and throwing it away
// on a transient network failure would turn a blip into a re-login.
func (s *Store) RefreshOAuth(provider string, mint func(cur *OAuthToken) (*OAuthToken, error)) (*OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lk, err := filelock.Acquire(s.lockPath())
	if err != nil {
		lk = nil // degrade to the in-process mutex; see mutateLocked
	}
	defer lk.Release()

	c, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	p := c.get(provider)
	if p == nil || p.OAuth == nil || p.OAuth.AccessToken == "" {
		return nil, fmt.Errorf("no stored oauth token for %s", provider)
	}
	cur := p.OAuth
	if !cur.NeedsRefresh() {
		// Someone refreshed while we queued. Theirs is the live grant.
		return cur, nil
	}
	next, err := mint(cur)
	if err != nil {
		return cur, err
	}
	if next == nil {
		return cur, nil
	}
	setOAuthIn(&c, provider, *next)
	if err := s.saveLocked(c); err != nil {
		return next, err
	}
	return next, nil
}

// Clear removes all credentials for provider.
func (s *Store) Clear(provider string) error {
	return s.Mutate(func(c *Credentials) {
		if _, ok := c.AdditionalAPIKeyCreds[provider]; ok {
			c.setAdditional(provider, ProviderCreds{})
			return
		}
		p := c.get(provider)
		if p == nil {
			c.setAdditional(provider, ProviderCreds{})
			return
		}
		*p = ProviderCreds{}
	})
}

// ClearAPIKey removes only the API key for provider, preserving any OAuth token.
func (s *Store) ClearAPIKey(provider string) error {
	return s.Mutate(func(c *Credentials) {
		if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
			cur.APIKey = ""
			c.setAdditional(provider, cur)
			return
		}
		if p := c.get(provider); p != nil {
			p.APIKey = ""
		}
	})
}

// ClearOAuth removes only the OAuth token for provider, preserving any API key.
func (s *Store) ClearOAuth(provider string) error {
	return s.Mutate(func(c *Credentials) {
		if cur, ok := c.AdditionalAPIKeyCreds[provider]; ok {
			cur.OAuth = nil
			c.setAdditional(provider, cur)
			return
		}
		if p := c.get(provider); p != nil {
			p.OAuth = nil
		}
	})
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
	// privfs.WriteFile, not a hand-rolled temp+rename. The fixed "auth.json.tmp"
	// this used was a shared name: two processes both O_TRUNC'd it and wrote
	// into it at once, so the rename could publish a blend of two writes rather
	// than either one. privfs uses os.CreateTemp, whose name no other writer
	// holds, and chmods 0600 before the rename so the window never widens.
	return privfs.WriteFile(s.path, b)
}
