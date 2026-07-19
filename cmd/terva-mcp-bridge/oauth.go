package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// This file is the OAuth 2.1 client the bridge exists for: the MCP authorization
// spec's discovery → dynamic client registration → authorization-code+PKCE →
// token-refresh chain, hand-rolled on stdlib so the bridge stays a standalone
// tool with no core coupling. The heavy, service-shaped parts core deliberately
// does not grow — a localhost redirect listener, a browser launch, a token vault
// — all live here.

// openBrowser is a seam: tests replace it to drive the redirect without a real
// browser. It returns an error if it cannot launch one; the caller still prints
// the URL so a headless or failed-launch user can open it by hand.
var openBrowser = defaultOpenBrowser

// --- discovery metadata shapes (RFC 9728 protected resource, RFC 8414 AS) ---

type protectedResourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type authServerMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// discover runs the MCP authorization discovery chain for a resource URL: find
// its authorization server (from the 401's WWW-Authenticate resource_metadata
// pointer, or the well-known protected-resource document), then fetch that
// server's metadata. wwwAuth may be "" (then only the well-known path is tried).
// The returned oauthClient has its endpoints and canonical resource filled but
// no client_id yet — registration is a separate step.
func discover(ctx context.Context, hc *http.Client, resourceURL, wwwAuth string) (oauthClient, error) {
	prm, err := discoverProtectedResource(ctx, hc, resourceURL, wwwAuth)
	if err != nil {
		return oauthClient{}, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return oauthClient{}, fmt.Errorf("bridge: %s advertises no authorization servers", resourceURL)
	}
	resource := prm.Resource
	if resource == "" {
		resource = canonicalResource(resourceURL)
	}
	// First advertised auth server wins; MCP servers typically list exactly one.
	as, err := discoverAuthServer(ctx, hc, prm.AuthorizationServers[0])
	if err != nil {
		return oauthClient{}, err
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return oauthClient{}, fmt.Errorf("bridge: authorization server %s is missing authorization_endpoint/token_endpoint", prm.AuthorizationServers[0])
	}
	return oauthClient{
		Issuer:                as.Issuer,
		AuthorizationEndpoint: as.AuthorizationEndpoint,
		TokenEndpoint:         as.TokenEndpoint,
		RegistrationEndpoint:  as.RegistrationEndpoint,
		Scopes:                as.ScopesSupported,
		Resource:              resource,
	}, nil
}

func discoverProtectedResource(ctx context.Context, hc *http.Client, resourceURL, wwwAuth string) (protectedResourceMeta, error) {
	// Prefer the exact metadata URL the server pointed at in its 401; fall back to
	// the well-known location derived from the resource origin.
	metaURL := resourceMetadataURL(wwwAuth)
	if metaURL == "" {
		u, err := url.Parse(resourceURL)
		if err != nil {
			return protectedResourceMeta{}, fmt.Errorf("bridge: bad resource url: %w", err)
		}
		metaURL = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
	}
	var prm protectedResourceMeta
	if err := getJSON(ctx, hc, metaURL, &prm); err != nil {
		return protectedResourceMeta{}, fmt.Errorf("bridge: protected-resource metadata (%s): %w", metaURL, err)
	}
	return prm, nil
}

func discoverAuthServer(ctx context.Context, hc *http.Client, issuer string) (authServerMeta, error) {
	issuer = strings.TrimRight(issuer, "/")
	// Try the OAuth 2.0 AS metadata location, then the OIDC one — servers publish
	// under one or the other.
	var lastErr error
	for _, suffix := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		var as authServerMeta
		if err := getJSON(ctx, hc, issuer+suffix, &as); err != nil {
			lastErr = err
			continue
		}
		if as.Issuer == "" {
			as.Issuer = issuer
		}
		return as, nil
	}
	return authServerMeta{}, fmt.Errorf("bridge: no authorization-server metadata at %s: %w", issuer, lastErr)
}

// registerClient performs RFC 7591 dynamic client registration when the server
// supports it, filling ClientID/ClientSecret on oc. A public client (PKCE, no
// secret) is requested — token_endpoint_auth_method "none". If the server has no
// registration endpoint the caller must supply a pre-provisioned client id.
func registerClient(ctx context.Context, hc *http.Client, oc *oauthClient, redirectURI string) error {
	if oc.RegistrationEndpoint == "" {
		return fmt.Errorf("bridge: server offers no dynamic client registration; provide a client id with --client-id")
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                "terva-mcp-bridge",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oc.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("bridge: client registration failed (%s): %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		return fmt.Errorf("bridge: unparseable registration response: %w", err)
	}
	if reg.ClientID == "" {
		return fmt.Errorf("bridge: registration returned no client_id")
	}
	oc.ClientID = reg.ClientID
	oc.ClientSecret = reg.ClientSecret
	return nil
}

// authorizeInteractive runs the full authorization-code + PKCE flow: it opens a
// localhost callback listener, sends the user's browser to the authorization
// endpoint, captures the redirect, and exchanges the code for tokens. This is the
// one-time interactive step; the resulting refresh token drives every later
// headless relay. hc is used for the token exchange; the browser talks to the
// authorization endpoint directly.
func authorizeInteractive(ctx context.Context, hc *http.Client, oc oauthClient, print func(string)) (storedTokens, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return storedTokens{}, fmt.Errorf("bridge: cannot open the OAuth callback listener: %w", err)
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	// A server with no pre-provisioned client registers dynamically now, using
	// this run's redirect URI.
	if oc.ClientID == "" {
		if err := registerClient(ctx, hc, &oc, redirectURI); err != nil {
			return storedTokens{}, err
		}
	}

	verifier, challenge := newPKCE()
	state, err := randToken(16)
	if err != nil {
		return storedTokens{}, err
	}

	authURL := buildAuthorizeURL(oc, redirectURI, challenge, state)
	print("terva-mcp-bridge: authorize access in your browser:\n  " + authURL)
	if berr := openBrowser(authURL); berr != nil {
		print("terva-mcp-bridge: could not open a browser automatically (" + berr.Error() + "); open the URL above manually.")
	}

	code, err := waitForCode(ctx, ln, state)
	if err != nil {
		return storedTokens{}, err
	}
	return exchangeCode(ctx, hc, oc, code, verifier, redirectURI)
}

// waitForCode serves the callback listener until the redirect arrives, validates
// state, and returns the authorization code. It answers the browser with a small
// page so the user knows to return to the terminal.
func waitForCode(ctx context.Context, ln net.Listener, wantState string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "Authorization failed: %s %s", e, q.Get("error_description"))
			done <- result{err: fmt.Errorf("bridge: authorization denied: %s %s", e, q.Get("error_description"))}
			return
		}
		if q.Get("state") != wantState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- result{err: fmt.Errorf("bridge: OAuth state mismatch — possible CSRF, aborting")}
			return
		}
		code := q.Get("code")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body><h3>terva-mcp-bridge: authorization complete.</h3>You can close this tab and return to the terminal.</body></html>")
		done <- result{code: code}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		if r.code == "" {
			return "", fmt.Errorf("bridge: callback carried no authorization code")
		}
		return r.code, nil
	}
}

// exchangeCode swaps the authorization code for tokens (authorization_code grant
// with the PKCE verifier and the RFC 8707 resource indicator).
func exchangeCode(ctx context.Context, hc *http.Client, oc oauthClient, code, verifier, redirectURI string) (storedTokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {oc.ClientID},
		"code_verifier": {verifier},
	}
	if oc.Resource != "" {
		form.Set("resource", oc.Resource)
	}
	return postToken(ctx, hc, oc, form)
}

// refreshTokens renews an access token from a refresh token. A server may or may
// not rotate the refresh token; if it returns a new one we keep it, otherwise the
// prior one carries forward.
func refreshTokens(ctx context.Context, hc *http.Client, oc oauthClient, refreshToken string) (storedTokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {oc.ClientID},
	}
	if oc.Resource != "" {
		form.Set("resource", oc.Resource)
	}
	t, err := postToken(ctx, hc, oc, form)
	if err != nil {
		return storedTokens{}, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, nil
}

// postToken performs a token-endpoint POST and maps the response to storedTokens
// with an absolute expiry. A confidential client (one that got a secret at
// registration) authenticates with client_secret_post.
func postToken(ctx context.Context, hc *http.Client, oc oauthClient, form url.Values) (storedTokens, error) {
	if oc.ClientSecret != "" {
		form.Set("client_secret", oc.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return storedTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return storedTokens{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return storedTokens{}, fmt.Errorf("bridge: token endpoint (%s): %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return storedTokens{}, fmt.Errorf("bridge: unparseable token response: %w", err)
	}
	if tr.AccessToken == "" {
		return storedTokens{}, fmt.Errorf("bridge: token response carried no access_token")
	}
	t := storedTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Client:       oc,
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// --- helpers ---

func buildAuthorizeURL(oc oauthClient, redirectURI, challenge, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {oc.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if len(oc.Scopes) > 0 {
		q.Set("scope", strings.Join(oc.Scopes, " "))
	}
	if oc.Resource != "" {
		q.Set("resource", oc.Resource)
	}
	sep := "?"
	if strings.Contains(oc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return oc.AuthorizationEndpoint + sep + q.Encode()
}

func newPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// resourceMetadataURL extracts the resource_metadata pointer from a
// WWW-Authenticate header, e.g. `Bearer resource_metadata="https://.../.well-known/..."`.
// Returns "" if absent.
func resourceMetadataURL(wwwAuth string) string {
	const key = `resource_metadata=`
	i := strings.Index(wwwAuth, key)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(wwwAuth[i+len(key):])
	rest = strings.TrimPrefix(rest, `"`)
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	if j := strings.IndexByte(rest, ','); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return strings.TrimSpace(rest)
}

// canonicalResource is the RFC 8707 resource indicator for an MCP endpoint: its
// URL without a fragment. Used when the protected-resource metadata omits an
// explicit resource value.
func canonicalResource(resourceURL string) string {
	u, err := url.Parse(resourceURL)
	if err != nil {
		return resourceURL
	}
	u.Fragment = ""
	return u.String()
}

func getJSON(ctx context.Context, hc *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func defaultOpenBrowser(u string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{u}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	default:
		cmd, args = "xdg-open", []string{u}
	}
	return exec.Command(cmd, args...).Start()
}
