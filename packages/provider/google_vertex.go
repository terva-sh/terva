package provider

// Google Vertex AI client.
//
// Wire format is identical to the public Generative Language API
// (generateContent / streamGenerateContent), so we reuse the existing
// geminiClient and rewrite the outgoing URL + auth via a RoundTripper.
//
// URL shape:
//
//	POST https://{location}-aiplatform.googleapis.com/v1/projects/{project}/
//	     locations/{location}/publishers/google/models/{model}:streamGenerateContent?alt=sse
//
// Auth: two paths supported.
//
//   - **API key** (GOOGLE_CLOUD_API_KEY): `x-goog-api-key` header. Simplest
//     setup; works for users who have created a Vertex AI API key in the
//     GCP console. We use this directly, no token exchange.
//   - **Service-account JSON** pointed to by GOOGLE_APPLICATION_CREDENTIALS:
//     we read the JSON, build a JWT signed with RS256 using the private
//     key, exchange it at `https://oauth2.googleapis.com/token` for a
//     short-lived (1h) access token, cache it in memory, and refresh on
//     demand. Sent as `Authorization: Bearer <token>`.
//   - **gcloud user OAuth** (~/.config/gcloud/application_default_credentials.json
//     with `type: "authorized_user"`): we read the JSON, pull the
//     client_id/client_secret/refresh_token, exchange the refresh token
//     at `https://oauth2.googleapis.com/token` for an access token,
//     same cache + refresh logic as service-account.
//
// Config env vars (read at construction time):
//   GOOGLE_CLOUD_PROJECT     required
//   GOOGLE_CLOUD_LOCATION    required (e.g. us-central1)
//   GOOGLE_CLOUD_API_KEY     optional; preferred over service-account
//   GOOGLE_APPLICATION_CREDENTIALS  optional; service-account JSON path

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type vertexConfig struct {
	project  string
	location string
	apiKey   string // empty if using OAuth-style auth

	// Auth mode. Exactly one of (saEmail+saPrivateKey) or
	// (userClientID+userClientSecret+userRefreshToken) is populated when
	// apiKey is empty.
	saEmail      string
	saPrivateKey *rsa.PrivateKey
	saTokenURI   string

	userClientID     string
	userClientSecret string
	userRefreshToken string
	userTokenURI     string
}

// cacheKey identifies the credential set for the token cache. We key on
// the auth principal (SA email or user client_id) so multiple configs
// in the same process don't trample each other.
func (c *vertexConfig) cacheKey() string {
	if c.saEmail != "" {
		return "sa:" + c.saEmail
	}
	return "user:" + c.userClientID
}

func loadVertexConfig() (*vertexConfig, error) {
	cfg := &vertexConfig{
		project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		location: os.Getenv("GOOGLE_CLOUD_LOCATION"),
		apiKey:   os.Getenv("GOOGLE_CLOUD_API_KEY"),
	}
	if cfg.location == "" {
		cfg.location = "us-central1"
	}
	if cfg.project == "" {
		return nil, fmt.Errorf("vertex: GOOGLE_CLOUD_PROJECT not set")
	}
	if cfg.apiKey != "" {
		return cfg, nil
	}
	// Try service-account JSON.
	credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credPath == "" {
		// ADC default path. Mirrors `gcloud auth application-default login`.
		if home, err := os.UserHomeDir(); err == nil {
			candidate := home + "/.config/gcloud/application_default_credentials.json"
			if _, err := os.Stat(candidate); err == nil {
				credPath = candidate
			}
		}
	}
	if credPath == "" {
		return nil, fmt.Errorf("vertex: no auth — set GOOGLE_CLOUD_API_KEY or GOOGLE_APPLICATION_CREDENTIALS")
	}
	b, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("vertex: read credentials %q: %w", credPath, err)
	}
	var raw struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		TokenURI     string `json:"token_uri"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("vertex: parse credentials: %w", err)
	}
	switch raw.Type {
	case "service_account":
		if raw.ClientEmail == "" || raw.PrivateKey == "" {
			return nil, fmt.Errorf("vertex: service_account JSON missing client_email or private_key")
		}
		block, _ := pem.Decode([]byte(raw.PrivateKey))
		if block == nil {
			return nil, fmt.Errorf("vertex: private_key is not PEM-encoded")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			// Try PKCS#1 as a fallback (older keys).
			k1, err2 := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err2 != nil {
				return nil, fmt.Errorf("vertex: parse private_key: %w", err)
			}
			cfg.saPrivateKey = k1
		} else {
			rsaKey, ok := key.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("vertex: private_key is not RSA")
			}
			cfg.saPrivateKey = rsaKey
		}
		cfg.saEmail = raw.ClientEmail
		cfg.saTokenURI = raw.TokenURI
		if cfg.saTokenURI == "" {
			cfg.saTokenURI = "https://oauth2.googleapis.com/token"
		}
	case "authorized_user":
		// gcloud auth application-default login produces this shape.
		if raw.ClientID == "" || raw.ClientSecret == "" || raw.RefreshToken == "" {
			return nil, fmt.Errorf("vertex: authorized_user JSON missing client_id/client_secret/refresh_token")
		}
		cfg.userClientID = raw.ClientID
		cfg.userClientSecret = raw.ClientSecret
		cfg.userRefreshToken = raw.RefreshToken
		cfg.userTokenURI = raw.TokenURI
		if cfg.userTokenURI == "" {
			cfg.userTokenURI = "https://oauth2.googleapis.com/token"
		}
	default:
		return nil, fmt.Errorf("vertex: unsupported credentials type %q (expected service_account or authorized_user)", raw.Type)
	}
	return cfg, nil
}

// vertexTokenCache caches one access token per service-account email.
type vertexTokenCache struct {
	mu     sync.Mutex
	tokens map[string]struct {
		value     string
		expiresAt time.Time
	}
	http *http.Client
}

var vertexCache = &vertexTokenCache{
	tokens: map[string]struct {
		value     string
		expiresAt time.Time
	}{},
	http: &http.Client{Timeout: 30 * time.Second},
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signServiceAccountJWT builds and signs a JWT for the
// `urn:ietf:params:oauth:grant-type:jwt-bearer` token exchange.
func signServiceAccountJWT(cfg *vertexConfig) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	now := time.Now().Unix()
	claims := map[string]any{
		"iss":   cfg.saEmail,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   cfg.saTokenURI,
		"exp":   now + 3600,
		"iat":   now,
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := b64url(hb) + "." + b64url(cb)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, cfg.saPrivateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

func (c *vertexTokenCache) get(ctx context.Context, cfg *vertexConfig) (string, error) {
	key := cfg.cacheKey()
	c.mu.Lock()
	tok, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && time.Now().Before(tok.expiresAt) {
		return tok.value, nil
	}

	var form url.Values
	var endpoint string
	switch {
	case cfg.saPrivateKey != nil:
		jwt, err := signServiceAccountJWT(cfg)
		if err != nil {
			return "", fmt.Errorf("vertex: sign jwt: %w", err)
		}
		form = url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"assertion":  {jwt},
		}
		endpoint = cfg.saTokenURI
	case cfg.userRefreshToken != "":
		form = url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {cfg.userClientID},
			"client_secret": {cfg.userClientSecret},
			"refresh_token": {cfg.userRefreshToken},
		}
		endpoint = cfg.userTokenURI
	default:
		return "", fmt.Errorf("vertex: no usable auth (need service_account or authorized_user creds)")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("vertex: token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := readBodyCapped(resp.Body, maxDiscoveryBodyBytes)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vertex: token exchange http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("vertex: parse token: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("vertex: empty access token")
	}
	exp := time.Now().Add(time.Duration(out.ExpiresIn-60) * time.Second)
	c.mu.Lock()
	c.tokens[key] = struct {
		value     string
		expiresAt time.Time
	}{out.AccessToken, exp}
	c.mu.Unlock()
	return out.AccessToken, nil
}

// vertexTransport rewrites the outgoing Gemini URL into the Vertex shape
// and replaces the x-goog-api-key header with either another
// x-goog-api-key (for the API-key flow) or Authorization: Bearer (for
// the service-account flow).
type vertexTransport struct {
	inner http.RoundTripper
	cfg   *vertexConfig
}

func (t *vertexTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Path looks like /v1beta/models/{model}:streamGenerateContent. Pull
	// out the model id and rewrite.
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(req.URL.Path, prefix) {
		return nil, fmt.Errorf("vertex: unexpected request path %q", req.URL.Path)
	}
	rest := strings.TrimPrefix(req.URL.Path, prefix)
	// rest = "<model>:streamGenerateContent"
	sep := strings.IndexByte(rest, ':')
	if sep < 0 {
		return nil, fmt.Errorf("vertex: malformed request path %q", req.URL.Path)
	}
	modelID := rest[:sep]
	verb := rest[sep:] // ":streamGenerateContent"
	newPath := fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/google/models/%s%s",
		t.cfg.project, t.cfg.location, modelID, verb)
	clone := req.Clone(req.Context())
	clone.URL.Host = t.cfg.location + "-aiplatform.googleapis.com"
	clone.URL.Path = newPath
	// Drop existing auth header and pick the right one.
	clone.Header.Del("x-goog-api-key")
	if t.cfg.apiKey != "" {
		clone.Header.Set("x-goog-api-key", t.cfg.apiKey)
	} else {
		tok, err := vertexCache.get(req.Context(), t.cfg)
		if err != nil {
			return nil, err
		}
		clone.Header.Set("Authorization", "Bearer "+tok)
	}
	return t.inner.RoundTrip(clone)
}

// NewVertex returns a Vertex AI client. The apiKey argument is ignored
// in favor of env-based config (GOOGLE_CLOUD_API_KEY or
// GOOGLE_APPLICATION_CREDENTIALS), since Vertex's auth model doesn't fit
// the "just paste a key" interface other providers use.
func NewVertex(_ string, _ string) Client {
	cfg, err := loadVertexConfig()
	if err != nil {
		return &unimplementedClient{name: "google-vertex", hint: err.Error(), wire: reasoningWireGemini}
	}
	inner := &geminiClient{
		apiKey:  "vertex-placeholder", // overwritten by transport
		baseURL: "https://" + cfg.location + "-aiplatform.googleapis.com",
		http: &http.Client{
			Transport: &vertexTransport{inner: http.DefaultTransport, cfg: cfg},
			Timeout:   0,
		},
	}
	// Wrap so Name() reports "google-vertex" instead of "google".
	return &renamedClient{inner: inner, name: "google-vertex"}
}

// renamedClient wraps a Client and overrides its Name(). Used for
// provider-id remapping (Vertex reuses gemini's wire client but should
// report as google-vertex for cost / cache / log purposes).
type renamedClient struct {
	inner Client
	name  string
}

func (r *renamedClient) Name() string { return r.name }
func (r *renamedClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return r.inner.Stream(ctx, req)
}

// Unwrap exposes the wrapped client so capability probes (e.g.
// ClientMirrorsToolImages) can see through the rename layer. Without
// this, the MirrorsToolImages() capability of the inner codex/gemini
// client is hidden behind renamedClient and tool-result image mirroring
// silently never fires for openai-responses / google-vertex.
func (r *renamedClient) Unwrap() Client { return r.inner }
