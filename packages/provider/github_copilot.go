package provider

// GitHub Copilot client.
//
// Auth chain:
//
//  1. User stores a GitHub Personal Access Token (PAT) in
//     COPILOT_GITHUB_TOKEN (or via terva's auth.json, when wired). The PAT
//     is the long-lived credential; it never hits the inference endpoint.
//  2. We trade the PAT for a short-lived Copilot token by hitting
//     `GET https://api.github.com/copilot_internal/v2/token` with the PAT
//     as `Authorization: Bearer` plus the Copilot identity headers
//     (Editor-Version, Editor-Plugin-Version, Copilot-Integration-Id,
//     User-Agent). The response carries `{ "token": "...", "expires_at": <unix> }`.
//  3. The short-lived token's value embeds a `proxy-ep=<host>` field that
//     tells us the real API host (individual users: api.individual.githubcopilot.com).
//  4. Inference requests go to `<host>/chat/completions` with the
//     short-lived token in `Authorization: Bearer` plus extras:
//       - X-Initiator: user|agent
//       - Openai-Intent: conversation-edits
//       - Copilot-Vision-Request: true (when images present; not wired
//         here because the terva openai client currently sends images inline)
//
// Token caching: short-lived tokens last ~30min. We cache one per PAT in
// memory for the process lifetime and refresh on demand. No disk cache.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Copilot identity headers — these must match what the VS Code Copilot
// extension sends or the proxy rejects with 401.
var copilotIdentityHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

type copilotToken struct {
	value     string
	expiresAt time.Time
	baseURL   string
}

// copilotTokenCache is a process-wide cache of short-lived Copilot tokens
// keyed by the user's PAT. Concurrency-safe; safe to call from multiple
// goroutines (a single agent loop is sequential, but extension intercepts
// can run in parallel).
type copilotTokenCache struct {
	mu     sync.Mutex
	tokens map[string]copilotToken
	http   *http.Client
}

var copilotCache = &copilotTokenCache{
	tokens: map[string]copilotToken{},
	http:   &http.Client{Timeout: 30 * time.Second},
}

// exchange swaps a PAT for a short-lived Copilot token. Caller is
// expected to hold no lock; we acquire/release internally.
func (c *copilotTokenCache) exchange(ctx context.Context, pat string) (copilotToken, error) {
	url := "https://api.github.com/copilot_internal/v2/token"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return copilotToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotIdentityHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return copilotToken{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := readBodyCapped(resp.Body, maxDiscoveryBodyBytes)
	if resp.StatusCode != http.StatusOK {
		return copilotToken{}, fmt.Errorf("copilot token exchange: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return copilotToken{}, fmt.Errorf("copilot token: parse: %w", err)
	}
	if out.Token == "" {
		return copilotToken{}, fmt.Errorf("copilot token: empty")
	}
	base := parseCopilotProxyEndpoint(out.Token)
	if base == "" {
		base = "https://api.individual.githubcopilot.com"
	}
	exp := time.Unix(out.ExpiresAt, 0)
	if exp.IsZero() || time.Until(exp) < 0 {
		// Reference subtracts a 5min safety margin; we do the same so a
		// long-running tool call doesn't blow past expiry mid-flight.
		exp = time.Now().Add(25 * time.Minute)
	} else {
		exp = exp.Add(-5 * time.Minute)
	}
	return copilotToken{value: out.Token, expiresAt: exp, baseURL: base}, nil
}

// get returns a fresh-enough Copilot token, refreshing if needed.
func (c *copilotTokenCache) get(ctx context.Context, pat string) (copilotToken, error) {
	c.mu.Lock()
	tok, ok := c.tokens[pat]
	c.mu.Unlock()
	if ok && time.Now().Before(tok.expiresAt) {
		return tok, nil
	}
	fresh, err := c.exchange(ctx, pat)
	if err != nil {
		return copilotToken{}, err
	}
	c.mu.Lock()
	c.tokens[pat] = fresh
	c.mu.Unlock()
	return fresh, nil
}

// parseCopilotProxyEndpoint extracts `proxy-ep=<host>` from a Copilot
// short-lived token's value (it's not a JWT — it's an ad-hoc
// semicolon-separated key=value string) and converts proxy.* -> api.*.
// Returns "" if not found.
func parseCopilotProxyEndpoint(token string) string {
	for _, part := range strings.Split(token, ";") {
		if strings.HasPrefix(part, "proxy-ep=") {
			host := strings.TrimPrefix(part, "proxy-ep=")
			host = strings.TrimPrefix(host, "proxy.")
			return "https://api." + host
		}
	}
	return ""
}

// copilotRefreshTransport wraps the default HTTP transport so every
// outgoing inference request gets the latest short-lived Copilot token
// in Authorization. Token exchange uses the PAT carried in the
// transport itself, not the request.
type copilotRefreshTransport struct {
	inner http.RoundTripper
	pat   string
}

func (t *copilotRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := copilotCache.get(req.Context(), t.pat)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+tok.value)
	// Identity headers also required on inference requests.
	for k, v := range copilotIdentityHeaders {
		clone.Header.Set(k, v)
	}
	clone.Header.Set("X-Initiator", "agent")
	clone.Header.Set("Openai-Intent", "conversation-edits")
	// If the request URL host doesn't match the token's proxy-ep, rewrite
	// it. The openaiClient pinned a static host at construction time, but
	// the canonical host comes from the token.
	if tok.baseURL != "" {
		if u, err := url.Parse(tok.baseURL); err == nil && u.Host != clone.URL.Host {
			clone.URL.Scheme = u.Scheme
			clone.URL.Host = u.Host
		}
	}
	return t.inner.RoundTrip(clone)
}

// NewGithubCopilotClient returns a Copilot-pinned OpenAI-compat client.
// The pat must be a GitHub Personal Access Token with Copilot access.
func NewGithubCopilotClient(pat string) Client {
	httpClient := &http.Client{
		Transport: &copilotRefreshTransport{inner: http.DefaultTransport, pat: pat},
		Timeout:   0,
	}
	// Initial baseURL is a sane default; copilotRefreshTransport rewrites
	// the host on every request based on the freshly-issued token.
	return &openaiClient{
		cred:    StaticCredential(pat), // unused at the wire level (transport overrides Auth) but kept for parity
		baseURL: "https://api.individual.githubcopilot.com",
		name:    "github-copilot",
		http:    httpClient,
	}
}
