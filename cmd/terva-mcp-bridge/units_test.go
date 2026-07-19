package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestResourceMetadataURL pins the WWW-Authenticate parsing: the discovery
// pointer is extracted whether quoted, comma-terminated, or bare, and absence
// yields "" (then the well-known fallback path is used).
func TestResourceMetadataURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Bearer resource_metadata="https://x/.well-known/oauth-protected-resource"`, "https://x/.well-known/oauth-protected-resource"},
		{`Bearer realm="x", resource_metadata="https://y/meta", scope="mcp"`, "https://y/meta"},
		{`Bearer resource_metadata=https://z/meta`, "https://z/meta"},
		{`Bearer realm="x"`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := resourceMetadataURL(c.in); got != c.want {
			t.Errorf("resourceMetadataURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPKCEChallengeDerivation: the challenge must be base64url(sha256(verifier))
// with no padding — the exact relation the authorization server re-checks.
func TestPKCEChallengeDerivation(t *testing.T) {
	verifier, challenge := newPKCE()
	if verifier == "" || challenge == "" {
		t.Fatal("empty PKCE pair")
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Errorf("challenge = %q, want base64url(sha256(verifier)) = %q", challenge, want)
	}
	if strings.ContainsAny(challenge, "=+/") {
		t.Errorf("challenge %q is not URL-safe unpadded base64", challenge)
	}
}

// TestReadResponseFrameSSE: a tools/call answered as an SSE stream is parsed to
// the frame carrying the request's id, even when a progress notification precedes
// it — the reason readResponseFrame matches on id rather than taking the first.
func TestReadResponseFrameSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A server-initiated notification (no id) then the real response (id 7).
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n"))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := readResponseFrame(resp, []byte(`7`))
	if err != nil {
		t.Fatalf("readResponseFrame: %v", err)
	}
	if !strings.Contains(string(frame), `"id":7`) || !strings.Contains(string(frame), `"ok":true`) {
		t.Errorf("SSE frame = %q, want the id-7 response, not the progress notification", frame)
	}
}

// TestTokenStoreRoundTripAndPerms: tokens persist and reload intact, and the file
// is 0600 — a world-readable token file would leak the credential.
func TestTokenStoreRoundTripAndPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	dir := testsupport.TempDir(t)
	store, err := newTokenStore(dir, "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	want := storedTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Round(time.Second),
		Client:       oauthClient{ClientID: "c", TokenEndpoint: "https://as/token", Resource: "https://mcp.example.com/mcp"},
	}
	if err := store.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perms = %o, want 600", perm)
	}
	got, ok, err := store.load()
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" || got.Client.ClientID != "c" {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

// TestResourceKeyDisambiguates: two MCP servers on the SAME host but different
// paths must get distinct token directories, or one would clobber the other.
func TestResourceKeyDisambiguates(t *testing.T) {
	a, err1 := resourceKey("https://host.example/mcp")
	b, err2 := resourceKey("https://host.example/other")
	if err1 != nil || err2 != nil {
		t.Fatalf("resourceKey errored: %v %v", err1, err2)
	}
	if a == b {
		t.Errorf("same-host different-path resources collided on key %q", a)
	}
	if !strings.HasPrefix(a, "host.example-") {
		t.Errorf("key %q should start with the sanitized host", a)
	}
}

// TestExpiredSkew: a token inside the refresh skew window is treated as expired
// (refreshed proactively), while a far-future one is live and a zero-expiry one is
// assumed live (the server will 401 if not).
func TestExpiredSkew(t *testing.T) {
	if (storedTokens{Expiry: time.Now().Add(10 * time.Second)}).expired() != true {
		t.Error("a token expiring within the skew must read as expired")
	}
	if (storedTokens{Expiry: time.Now().Add(time.Hour)}).expired() != false {
		t.Error("a far-future token must read as live")
	}
	if (storedTokens{}).expired() != false {
		t.Error("a zero-expiry token must read as live")
	}
}

// TestParseArgs covers the CLI surface the config and operators depend on.
func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"https://x/mcp"})
	if err != nil || o.mode != "relay" || o.remoteURL != "https://x/mcp" {
		t.Fatalf("relay parse: %+v err=%v", o, err)
	}
	o, err = parseArgs([]string{"login", "https://x/mcp", "--client-id", "cid", "--header", "X-Team: alpha"})
	if err != nil || o.mode != "login" || o.clientID != "cid" || o.headers["X-Team"] != "alpha" {
		t.Fatalf("login parse: %+v err=%v", o, err)
	}
	if _, err := parseArgs([]string{}); err == nil {
		t.Error("no url must error")
	}
	if _, err := parseArgs([]string{"ftp://x"}); err == nil {
		t.Error("non-http url must error")
	}
	if _, err := parseArgs([]string{"a", "b"}); err == nil {
		t.Error("two positionals must error")
	}
}
