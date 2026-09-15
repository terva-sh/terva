// Package oidc is terva's OpenID Connect relying party: the half that turns "a
// browser showed up" into an identity and a set of terva roles.
//
// 🔑 The PROTOCOL is not ours. Discovery, JWKS fetch and rotation, ID-token
// signature verification, and the authorization-code exchange all come from
// github.com/coreos/go-oidc and golang.org/x/oauth2. What lives here is only
// what is genuinely terva's: the configuration shape, the group→role mapping,
// and the refusal to talk to a plaintext issuer.
//
// That split is a deliberate REVERSAL. An earlier pass hand-rolled the whole
// flow and leaned on OIDC Core §3.1.3.7 item 6 — TLS in place of signature
// verification for a token fetched directly from the token endpoint. That is
// spec-legal, and it was the wrong shape for this project: it holds only while
// every token arrives by direct exchange, so it left terva one feature away (a
// back-channel logout, a client-supplied token) from needing the crypto it had
// declined to write. Auth is not an area to stretch not-invented-here.
//
// ⚠️ This is the first break in the standing out-of-tree directive — "zero new
// dependencies across 25k added lines", recorded in the upstream-divergence
// chapter of docs/architecture/. Deliberate, and for the one domain where
// writing it yourself is the riskier engineering choice.
//
// Nothing here names a vendor: Authentik is what it was built against, and every
// provider-specific URL comes from `.well-known/openid-configuration`.
//
// See docs/proposals/daemon-access-auth.md (step 3).
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is the relying-party configuration an operator writes.
type Config struct {
	// Issuer is the IdP's issuer URL; discovery hangs off it.
	Issuer string `json:"issuer"`
	// ClientID identifies terva to the IdP.
	ClientID string `json:"client_id"`
	// ClientSecret is optional: a public client authenticates with PKCE alone.
	ClientSecret string `json:"client_secret,omitempty"`
	// Scopes defaults to profile+email+groups. "openid" is always sent.
	Scopes []string `json:"scopes,omitempty"`
	// GroupsClaim names the ID-token claim carrying the user's groups.
	// Defaults to "groups", which is what Authentik and Keycloak both use.
	GroupsClaim string `json:"groups_claim,omitempty"`
	// RoleMap maps an IdP group name to a terva role. A user with no mapped
	// group authenticates successfully and is granted nothing — see [Provider.Roles].
	RoleMap map[string]string `json:"role_map,omitempty"`
	// AllowInsecureIssuer permits a plaintext http:// issuer.
	//
	// 🚨 DEVELOPMENT ONLY, and signature verification does NOT make it safe.
	// Discovery over plaintext is unauthenticated, so whoever can rewrite it can
	// point terva's JWKS fetch at their own keys and then sign any identity they
	// like — the signature check would pass. Never set this on anything reachable.
	AllowInsecureIssuer bool `json:"allow_insecure_issuer,omitempty"`
}

// Provider is a discovered IdP ready to authenticate browsers.
type Provider struct {
	cfg      Config
	verifier *gooidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// Discover fetches the IdP's metadata and returns a ready Provider.
//
// hc, when non-nil, is used for discovery, the JWKS fetch and the token
// exchange — so a test can hand over an httptest client, and a deployment
// fronted by a private CA can hand over one carrying its pool.
func Discover(ctx context.Context, cfg Config, redirectURI string, hc *http.Client) (*Provider, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, fmt.Errorf("oidc: issuer is empty")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, fmt.Errorf("oidc: client_id is empty")
	}
	if err := requireSecure(cfg.Issuer, cfg.AllowInsecureIssuer); err != nil {
		return nil, err
	}
	if hc != nil {
		ctx = gooidc.ClientContext(ctx, hc)
	}
	// NewProvider fetches .well-known/openid-configuration and checks the
	// discovered issuer against the requested one — the IdP-mix-up defense. A
	// metadata document naming a different issuer would otherwise redirect the
	// token exchange, handing an authorization code to whoever it named.
	p, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery: %w", err)
	}
	ep := p.Endpoint()
	for _, u := range []string{ep.AuthURL, ep.TokenURL} {
		if err := requireSecure(u, cfg.AllowInsecureIssuer); err != nil {
			return nil, err
		}
	}
	return &Provider{
		cfg: cfg,
		// Verifies signature, issuer, audience and expiry against the IdP's
		// published keys. The keyset is remote and the library owns its
		// lifetime — caching, rotation, and refetching on an unknown kid.
		//
		// The algorithm list is pinned rather than taken from discovery. Left
		// empty, the library accepts whatever the IdP advertises, and an IdP
		// that lists HS256 alongside RS256 (Authentik and Keycloak both can)
		// would let a token NAME an HMAC algorithm against a keyset of public
		// keys — the classic key-confusion shape. Today the JOSE layer beneath
		// the library refuses an HMAC algorithm against a public key on its
		// own, and the pin is measured against that: without it the confusion
		// test still passes. It is here so that acceptance depends on neither
		// a lower layer's key-type check nor what the IdP chooses to advertise.
		// The tests in oidc_test.go pin the three failure modes decision 0013
		// names: `none`, algorithm confusion, and `kid` handling.
		verifier: p.Verifier(&gooidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: asymmetricSigningAlgs,
		}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     ep,
			RedirectURL:  redirectURI,
			Scopes:       scopes(cfg.Scopes),
		},
	}, nil
}

// asymmetricSigningAlgs is every JWS algorithm an ID token may carry. All are
// public-key schemes: a token can only be minted by whoever holds the IdP's
// private key, and nothing terva holds can produce a valid signature. HMAC
// algorithms are absent on purpose, and "none" is never in the list.
var asymmetricSigningAlgs = []string{
	gooidc.RS256, gooidc.RS384, gooidc.RS512,
	gooidc.ES256, gooidc.ES384, gooidc.ES512,
	gooidc.PS256, gooidc.PS384, gooidc.PS512,
	gooidc.EdDSA,
}

// requireSecure refuses a plaintext endpoint unless the operator opted out.
func requireSecure(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("oidc: unparseable url %q: %w", raw, err)
	}
	if u.Scheme == "https" || allowInsecure {
		return nil
	}
	return fmt.Errorf("oidc: %q is not https; discovery and the JWKS fetch would be unauthenticated, so a signature check proves nothing (set allow_insecure_issuer only for local development)", raw)
}

// scopes guarantees "openid" is present exactly once.
func scopes(want []string) []string {
	if len(want) == 0 {
		want = []string{"profile", "email", "groups"}
	}
	out := []string{gooidc.ScopeOpenID}
	seen := map[string]bool{gooidc.ScopeOpenID: true}
	for _, s := range want {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// NewLoginSecrets mints the per-attempt values: a PKCE verifier and a nonce.
// Both must be stashed against the browser and handed back to [Provider.Exchange],
// which is what ties a callback to the attempt that started it.
func NewLoginSecrets() (pkceVerifier, nonce string) {
	return oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
}

// AuthorizeURL is where the browser goes to log in.
func (p *Provider) AuthorizeURL(state, nonce, pkceVerifier string) string {
	return p.oauth.AuthCodeURL(state,
		oauth2.S256ChallengeOption(pkceVerifier),
		gooidc.Nonce(nonce),
	)
}

// Identity is what a completed login establishes.
type Identity struct {
	// Subject is the IdP's stable identifier. 🔑 Everything downstream keys on
	// this and never on email or username: both are mutable in every IdP, so
	// keying on them means a rename either strands a user's environment or —
	// worse — lets a recycled address inherit someone else's.
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// Exchange trades an authorization code for a verified identity.
//
// Signature, issuer, audience and expiry are checked by the library against the
// IdP's published keys. The nonce is checked here because only the caller knows
// which login attempt this browser started.
func (p *Provider) Exchange(ctx context.Context, code, nonce, pkceVerifier string, hc *http.Client) (*Identity, error) {
	if hc != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	}
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return nil, fmt.Errorf("oidc: token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("oidc: token response carried no id_token; is the openid scope granted for this client?")
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("oidc: id_token: %w", err)
	}
	// The nonce ties this token to the login attempt that started in THIS
	// browser; without it a token minted for another session could be replayed
	// into this callback. The library cannot check it — it does not know what we
	// sent — so this one stays ours.
	if nonce != "" && idt.Nonce != nonce {
		return nil, fmt.Errorf("oidc: id_token nonce does not match this login attempt")
	}
	if strings.TrimSpace(idt.Subject) == "" {
		return nil, fmt.Errorf("oidc: id_token has no sub")
	}
	var claims map[string]json.RawMessage
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: id_token claims: %w", err)
	}
	return &Identity{
		Subject: idt.Subject,
		Email:   stringClaim(claims, "email"),
		Name:    firstNonEmpty(stringClaim(claims, "name"), stringClaim(claims, "preferred_username")),
		Groups:  groupsFrom(claims, p.groupsClaim()),
	}, nil
}

func (p *Provider) groupsClaim() string {
	if c := strings.TrimSpace(p.cfg.GroupsClaim); c != "" {
		return c
	}
	return "groups"
}

func stringClaim(claims map[string]json.RawMessage, name string) string {
	raw, ok := claims[name]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// groupsFrom reads the configured groups claim, tolerating the two shapes IdPs
// actually emit: a list of strings, or a single string.
func groupsFrom(claims map[string]json.RawMessage, claim string) []string {
	raw, ok := claims[claim]
	if !ok {
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		return []string{one}
	}
	return nil
}

// Roles maps an identity's groups to terva role names through the configured
// RoleMap.
//
// 🔑 An unmapped user gets NOTHING — not a default role. Authenticating proves
// who someone is; it says nothing about whether this daemon's operator meant to
// give them access. Defaulting here would mean everyone the IdP admits becomes a
// terva user, which is the opposite of the decision an operator makes by writing
// a role map at all.
func (p *Provider) Roles(id *Identity) []string {
	if id == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range id.Groups {
		role, ok := p.cfg.RoleMap[g]
		if !ok || role == "" || seen[role] {
			continue
		}
		seen[role] = true
		out = append(out, role)
	}
	return out
}
