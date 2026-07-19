package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// authSource supplies the Authorization header for upstream requests and renews
// it when the server rejects the current credential (a 401). It is the seam
// between the static-auth fallback and the full OAuth path, so upstream's
// request logic is auth-agnostic.
type authSource interface {
	// header returns the Authorization value to send ("" for no auth). It may
	// refresh a soon-to-expire token proactively.
	header(ctx context.Context) (string, error)
	// reauth renews the credential after a 401; a nil return means "retry is
	// worth it", an error means the credential cannot be renewed headlessly.
	reauth(ctx context.Context) error
}

// noAuth is the null source: no Authorization header, nothing to renew. Used
// for a server that needs no auth at all.
type noAuth struct{}

func (noAuth) header(context.Context) (string, error) { return "", nil }
func (noAuth) reauth(context.Context) error {
	return fmt.Errorf("bridge: server rejected the request and there is no credential to renew")
}

// staticAuth sends a fixed Authorization value (a bearer token from an env var,
// or an operator-supplied header). It cannot self-renew — a 401 is terminal, and
// the error tells the operator the token is stale.
type staticAuth struct{ value string }

func (s staticAuth) header(context.Context) (string, error) { return s.value, nil }
func (s staticAuth) reauth(context.Context) error {
	return fmt.Errorf("bridge: static credential rejected (401); the token is invalid or expired")
}

// oauthSource is the headless relay's credential: stored OAuth tokens that it
// refreshes transparently — proactively when the access token is near expiry, and
// reactively on a 401. The refresh token and the discovered client both live in
// the store, so a relay needs no interactive step and no re-discovery. When the
// refresh token itself is gone or rejected, the error points the user at `login`.
type oauthSource struct {
	store *tokenStore
	hc    *http.Client

	mu   sync.Mutex
	toks storedTokens
}

func newOAuthSource(store *tokenStore, hc *http.Client, toks storedTokens) *oauthSource {
	return &oauthSource{store: store, hc: hc, toks: toks}
}

func (o *oauthSource) header(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.toks.expired() {
		if err := o.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	if o.toks.AccessToken == "" {
		return "", fmt.Errorf("bridge: no access token; run `terva-mcp-bridge login <url>` first")
	}
	return "Bearer " + o.toks.AccessToken, nil
}

func (o *oauthSource) reauth(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshLocked(ctx)
}

func (o *oauthSource) refreshLocked(ctx context.Context) error {
	if o.toks.RefreshToken == "" {
		return fmt.Errorf("bridge: token expired and no refresh token; run `terva-mcp-bridge login <url>` again")
	}
	t, err := refreshTokens(ctx, o.hc, o.toks.Client, o.toks.RefreshToken)
	if err != nil {
		return fmt.Errorf("bridge: token refresh failed (run `terva-mcp-bridge login <url>` to re-authorize): %w", err)
	}
	o.toks = t
	return o.store.save(t)
}
