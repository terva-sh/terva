package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// fakeOAuthMCP is a complete stand-in for an OAuth-gated remote MCP server: it
// serves the discovery documents (RFC 9728 protected-resource, RFC 8414 auth
// server), dynamic client registration (RFC 7591), an /authorize endpoint that
// auto-consents by redirecting back with a code, a /token endpoint that verifies
// PKCE and issues + refreshes tokens, and the /mcp endpoint itself — which 401s
// (with a discovery-pointing WWW-Authenticate) until a valid bearer is presented.
// It is enough to drive the bridge's entire flow with no real browser or network.
type fakeOAuthMCP struct {
	srv *httptest.Server

	mu            sync.Mutex
	codeChallenge map[string]string // authorization code -> PKCE challenge
	accessToken   string            // the currently-valid access token
	refreshToken  string
	registered    bool
	refreshes     int
	lastAuthHdr   string // Authorization seen on the last /mcp call
	expiresIn     int    // access-token lifetime handed out at /token (0 = unset)
}

func newFakeOAuthMCP(t *testing.T) *fakeOAuthMCP {
	t.Helper()
	f := &fakeOAuthMCP{codeChallenge: map[string]string{}, expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", f.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", f.handleAuthServerMeta)
	mux.HandleFunc("/register", f.handleRegister)
	mux.HandleFunc("/authorize", f.handleAuthorize)
	mux.HandleFunc("/token", f.handleToken)
	mux.HandleFunc("/mcp", f.handleMCP)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuthMCP) mcpURL() string { return f.srv.URL + "/mcp" }

func (f *fakeOAuthMCP) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"resource":              f.mcpURL(),
		"authorization_servers": []string{f.srv.URL},
	})
}

func (f *fakeOAuthMCP) handleAuthServerMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                 f.srv.URL,
		"authorization_endpoint": f.srv.URL + "/authorize",
		"token_endpoint":         f.srv.URL + "/token",
		"registration_endpoint":  f.srv.URL + "/register",
		"scopes_supported":       []string{"mcp"},
	})
}

func (f *fakeOAuthMCP) handleRegister(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.registered = true
	f.mu.Unlock()
	writeJSON(w, map[string]any{"client_id": "dyn-client-123"})
}

// handleAuthorize auto-consents: it records the PKCE challenge under a fresh code
// and 302s straight back to the redirect_uri — the test's stand-in for a user
// clicking "allow" instantly.
func (f *fakeOAuthMCP) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "PKCE required", http.StatusBadRequest)
		return
	}
	code := "auth-code-" + q.Get("state")
	f.mu.Lock()
	f.codeChallenge[code] = q.Get("code_challenge")
	f.mu.Unlock()
	redirect := q.Get("redirect_uri") + "?code=" + code + "&state=" + q.Get("state")
	http.Redirect(w, r, redirect, http.StatusFound)
}

func (f *fakeOAuthMCP) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		challenge, ok := f.codeChallenge[r.Form.Get("code")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		// Verify PKCE: base64url(sha256(verifier)) must equal the stored challenge.
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			http.Error(w, "PKCE verification failed", http.StatusBadRequest)
			return
		}
		f.issueTokens(w)
	case "refresh_token":
		f.mu.Lock()
		valid := r.Form.Get("refresh_token") == f.refreshToken
		f.refreshes++
		f.mu.Unlock()
		if !valid {
			http.Error(w, "bad refresh token", http.StatusBadRequest)
			return
		}
		f.issueTokens(w)
	default:
		http.Error(w, "unsupported grant", http.StatusBadRequest)
	}
}

func (f *fakeOAuthMCP) issueTokens(w http.ResponseWriter) {
	f.mu.Lock()
	f.accessToken = fmt.Sprintf("access-%d", f.refreshes)
	f.refreshToken = "refresh-tok"
	access := f.accessToken
	expiresIn := f.expiresIn
	f.mu.Unlock()
	writeJSON(w, map[string]any{
		"access_token":  access,
		"refresh_token": "refresh-tok",
		"token_type":    "Bearer",
		"expires_in":    expiresIn,
	})
}

// handleMCP is the resource server: it 401s (pointing at discovery) without the
// current valid bearer, and otherwise answers the tools-only MCP surface.
func (f *fakeOAuthMCP) handleMCP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	want := "Bearer " + f.accessToken
	hasToken := f.accessToken != ""
	f.lastAuthHdr = r.Header.Get("Authorization")
	got := f.lastAuthHdr
	f.mu.Unlock()

	if !hasToken || got != want {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.srv.URL+`/.well-known/oauth-protected-resource"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, _ := readAll(r)
	var req rpcRequest
	_ = json.Unmarshal(body, &req)
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-remote", "version": "1"},
		})
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{
			"tools": []map[string]any{{"name": "remote_echo", "description": "echoes", "inputSchema": map[string]any{"type": "object"}}},
		})
	case "tools/call":
		writeRPC(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "called remote_echo"}},
		})
	default:
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted) // notification
			return
		}
		writeRPC(w, req.ID, map[string]any{})
	}
}

// --- the end-to-end test: login, then relay a tools/call through the bridge ---

func TestBridgeLoginThenRelay(t *testing.T) {
	f := newFakeOAuthMCP(t)
	tokenDir := testsupport.TempDir(t)

	// A real browser is replaced by an async GET that follows the auto-consent
	// redirect into the bridge's localhost callback. Async because the callback
	// listener only starts serving after openBrowser returns.
	restore := openBrowser
	openBrowser = func(u string) error {
		go func() {
			resp, err := http.Get(u) //nolint
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = restore }()

	ctx := context.Background()
	opts := options{mode: "login", remoteURL: f.mcpURL(), tokenDir: tokenDir, headers: map[string]string{}}

	// 1. Login: probe -> discover -> register -> authorize -> token -> store.
	if err := runLogin(ctx, opts); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !f.registered {
		t.Error("dynamic client registration never happened")
	}
	store, _ := newTokenStore(tokenDir, f.mcpURL())
	toks, ok, err := store.load()
	if err != nil || !ok {
		t.Fatalf("tokens not persisted after login: ok=%v err=%v", ok, err)
	}
	if toks.AccessToken == "" || toks.RefreshToken == "" {
		t.Fatalf("stored tokens incomplete: %+v", toks)
	}

	// 2. Relay: build the upstream from the stored creds and drive the stdio
	// server the way terva would — initialize, tools/list, tools/call.
	auth := newOAuthSource(store, http.DefaultClient, toks)
	up := newUpstream(f.mcpURL(), http.DefaultClient, auth, nil)
	initRes, err := up.initialize(ctx)
	if err != nil {
		t.Fatalf("upstream initialize: %v", err)
	}

	responses := driveStdio(t, up, initRes, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remote_echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	})

	// initialize relayed the remote's real serverInfo.
	if got := string(responses["1"].Result); !strings.Contains(got, "fake-remote") {
		t.Errorf("initialize result did not carry the remote serverInfo: %s", got)
	}
	// tools/list exposed the remote tool 1:1.
	if got := string(responses["2"].Result); !strings.Contains(got, "remote_echo") {
		t.Errorf("tools/list did not relay the remote tool: %s", got)
	}
	// tools/call reached the remote and carried the bearer token.
	if got := string(responses["3"].Result); !strings.Contains(got, "called remote_echo") {
		t.Errorf("tools/call did not relay: %s", got)
	}
	if responses["4"].Result == nil {
		t.Errorf("ping got no result: %+v", responses["4"])
	}
	if f.lastAuthHdr != "Bearer "+f.accessToken {
		t.Errorf("upstream call carried Authorization %q, want the stored access token", f.lastAuthHdr)
	}
}

// TestBridgeTokenRefreshOn401: an access token the server no longer accepts is
// refreshed transparently (via the stored refresh token) and the call retried, so
// a stale token never surfaces to terva as a failure.
func TestBridgeTokenRefreshOn401(t *testing.T) {
	f := newFakeOAuthMCP(t)
	tokenDir := testsupport.TempDir(t)
	restore := openBrowser
	openBrowser = func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = restore }()

	ctx := context.Background()
	opts := options{mode: "login", remoteURL: f.mcpURL(), tokenDir: tokenDir, headers: map[string]string{}}
	if err := runLogin(ctx, opts); err != nil {
		t.Fatalf("login: %v", err)
	}
	store, _ := newTokenStore(tokenDir, f.mcpURL())
	toks, _, _ := store.load()

	// Simulate the access token going stale server-side: rotate it so the stored
	// one 401s, forcing a refresh on the next call.
	f.mu.Lock()
	f.accessToken = "rotated-server-side"
	f.mu.Unlock()

	auth := newOAuthSource(store, http.DefaultClient, toks)
	up := newUpstream(f.mcpURL(), http.DefaultClient, auth, nil)
	if _, err := up.initialize(ctx); err != nil {
		t.Fatalf("initialize should have refreshed through the 401: %v", err)
	}
	if f.refreshes == 0 {
		t.Error("no refresh happened; the 401 did not trigger a token refresh")
	}
	// The refreshed token was persisted for the next relay.
	reloaded, _, _ := store.load()
	if reloaded.AccessToken == toks.AccessToken {
		t.Error("refreshed access token was not written back to the store")
	}
}

// TestBridgeStaticAuth: the --bearer-env fallback (OQ-5) skips OAuth entirely and
// sends a fixed bearer, so the bridge is a complete alternative for a server the
// operator already has a token for.
func TestBridgeStaticAuth(t *testing.T) {
	f := newFakeOAuthMCP(t)
	// Pre-set the server's accepted token; the operator supplies the same via env.
	f.mu.Lock()
	f.accessToken = "operator-token"
	f.mu.Unlock()
	t.Setenv("MY_MCP_TOKEN", "operator-token")

	opts := options{mode: "relay", remoteURL: f.mcpURL(), bearerEnv: "MY_MCP_TOKEN", headers: map[string]string{}}
	auth, err := relayAuth(opts, http.DefaultClient)
	if err != nil {
		t.Fatalf("relayAuth (static): %v", err)
	}
	up := newUpstream(f.mcpURL(), http.DefaultClient, auth, nil)
	if _, err := up.initialize(context.Background()); err != nil {
		t.Fatalf("static-auth initialize: %v", err)
	}
	if f.lastAuthHdr != "Bearer operator-token" {
		t.Errorf("static auth sent %q, want the env bearer", f.lastAuthHdr)
	}
}

// TestRelayWithoutLoginFails: a relay with no stored tokens must refuse with a
// pointer to login, never silently proceed unauthenticated.
func TestRelayWithoutLoginFails(t *testing.T) {
	f := newFakeOAuthMCP(t)
	opts := options{mode: "relay", remoteURL: f.mcpURL(), tokenDir: testsupport.TempDir(t), headers: map[string]string{}}
	_, err := relayAuth(opts, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("relay without stored tokens should error pointing at login, got %v", err)
	}
}

// driveStdio feeds a set of JSON-RPC request lines through the stdio server and
// returns the responses keyed by id. serve() returns at EOF once every request
// has been handled.
func driveStdio(t *testing.T, up *upstream, initRes json.RawMessage, requests []string) map[string]rpcResponse {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	out := &syncBuffer{}
	srv := newStdioServer(up, initRes, in, out)
	if err := srv.serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	responses := map[string]rpcResponse{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		responses[strings.Trim(string(resp.ID), `"`)] = resp
	}
	return responses
}

// --- small helpers ---

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(result)
	_, _ = w.Write(resultResponse(id, b))
}

func readAll(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.Bytes(), err
}
