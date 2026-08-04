package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
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

// keyedHome writes an at-rest key into a fresh home, the way `terva secret
// init` would, and returns both.
func keyedHome(t *testing.T) (string, *age.X25519Identity) {
	t.Helper()
	dir := testsupport.TempDir(t)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, secrets.KeyFileName), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, id
}

// The bridge is a separate binary that deliberately imports no core packages,
// yet it holds OAuth access and refresh tokens plus a client_secret. With
// terva's key configured its store is sealed WHOLE — nothing but the bridge
// reads it, so there is no structure worth leaving legible.
func TestTokenStoreIsSealedWhenAKeyExists(t *testing.T) {
	dir, _ := keyedHome(t)
	store, err := newTokenStore(dir, "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	want := storedTokens{
		AccessToken:  "at-secret",
		RefreshToken: "rt-secret",
		Client:       oauthClient{ClientID: "c", ClientSecret: "cs-secret"},
	}
	if err := store.save(want); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(raw) {
		t.Fatal("token store is not encrypted on a home that has a key")
	}
	for _, leaked := range []string{"at-secret", "rt-secret", "cs-secret"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("%q is readable on disk", leaked)
		}
	}

	got, ok, err := store.load()
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.AccessToken != "at-secret" || got.RefreshToken != "rt-secret" || got.Client.ClientSecret != "cs-secret" {
		t.Errorf("round trip through the seal lost data: %+v", got)
	}
}

// The sniff is on CONTENT, not configuration: a store written before
// `terva secret init` must keep loading afterwards, and the next save seals it.
func TestPlaintextStoreStillLoadsAfterAKeyAppears(t *testing.T) {
	dir := testsupport.TempDir(t)
	store, err := newTokenStore(dir, "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(storedTokens{AccessToken: "before"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(store.path); secrets.IsAgeFile(raw) {
		t.Fatal("wrote ciphertext with no key configured")
	}

	// The operator runs `terva secret init` afterwards.
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, secrets.KeyFileName), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.load()
	if err != nil || !ok || got.AccessToken != "before" {
		t.Fatalf("plaintext store stopped loading once a key existed: %+v ok=%v err=%v", got, ok, err)
	}
	if err := store.save(got); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(store.path); !secrets.IsAgeFile(raw) {
		t.Fatal("the next save did not seal the store")
	}
}

// Ciphertext with no key must fail LOUDLY. Degrading to "no tokens" would look
// to the bridge like an expired session, and it would quietly re-authenticate
// — abandoning credentials that were only unreadable, never gone.
func TestSealedStoreWithNoKeyFailsClosed(t *testing.T) {
	dir, _ := keyedHome(t)
	store, err := newTokenStore(dir, "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(storedTokens{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, secrets.KeyFileName)); err != nil {
		t.Fatal(err)
	}

	_, ok, err := store.load()
	if err == nil {
		t.Fatal("an unreadable store loaded as empty; the bridge would re-authenticate over live credentials")
	}
	if ok {
		t.Error("reported usable tokens it could not read")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error does not say the file is encrypted: %v", err)
	}
}

// Two bridges refreshing the same resource at once used to write the same
// "<path>.tmp" scratch file, so one could rename the other's half-written bytes
// into place. The temp must be unique per writer.
func TestConcurrentSavesDoNotShareATempFile(t *testing.T) {
	dir, _ := keyedHome(t)
	store, err := newTokenStore(dir, "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.save(storedTokens{AccessToken: fmt.Sprintf("token-%d", i)}); err != nil {
				t.Errorf("save: %v", err)
			}
		}()
	}
	wg.Wait()

	got, ok, err := store.load()
	if err != nil || !ok {
		t.Fatalf("store unreadable after concurrent saves: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(got.AccessToken, "token-") {
		t.Errorf("store holds a torn value: %q", got.AccessToken)
	}
	// No scratch files survive.
	entries, err := os.ReadDir(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}
