package auth

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "auth.json"))

	if err := s.SetAPIKey("anthropic", "sk-ant-xyz"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Anthropic.APIKey != "sk-ant-xyz" {
		t.Fatalf("got %+v", c)
	}
	if !c.Has("anthropic") || c.Method("anthropic") != "apikey" {
		t.Fatalf("method mismatch: %q", c.Method("anthropic"))
	}

	tok := OAuthToken{AccessToken: "tok", RefreshToken: "ref", Expiry: time.Now().Add(1 * time.Hour)}
	if err := s.SetOAuth("openai", tok); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Load()
	if c.OpenAI.OAuth == nil || c.OpenAI.OAuth.AccessToken != "tok" {
		t.Fatalf("got %+v", c.OpenAI)
	}
	if c.Method("openai") != "oauth" {
		t.Fatalf("method=%q", c.Method("openai"))
	}

	if err := s.Clear("anthropic"); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Load()
	if c.Has("anthropic") {
		t.Fatalf("expected cleared, got %+v", c.Anthropic)
	}
}

func TestPKCE(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Verifier) < 40 || len(p.Challenge) < 40 {
		t.Fatalf("short pkce: %+v", p)
	}
	if p.Verifier == p.Challenge {
		t.Fatal("verifier == challenge")
	}
}

func TestAuthorizeURL(t *testing.T) {
	p, _ := NewPKCE()
	u, state, err := AnthropicOAuth.AuthorizeURL(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("client_id") != AnthropicOAuth.ClientID {
		t.Fatal("missing client_id")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatal("wrong challenge method")
	}
	// anthropic uses state == verifier
	if q.Get("state") != p.Verifier {
		t.Fatalf("state mismatch: %q vs verifier %q", q.Get("state"), p.Verifier)
	}
	if state != p.Verifier {
		t.Fatalf("returned state != verifier")
	}
	if q.Get("redirect_uri") != AnthropicOAuth.RedirectURI() {
		t.Fatalf("redirect_uri=%q", q.Get("redirect_uri"))
	}
	if q.Get("code") != "true" {
		t.Fatalf("missing code=true extra arg")
	}
}

func TestOpenAIAuthorizeURL(t *testing.T) {
	p, _ := NewPKCE()
	u, state, err := OpenAIOAuth.AuthorizeURL(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	q := parsed.Query()
	if q.Get("redirect_uri") != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri=%q", q.Get("redirect_uri"))
	}
	if state == p.Verifier {
		t.Fatal("openai state should be random, not verifier")
	}
	if q.Get("originator") != "terva" {
		t.Fatalf("originator=%q", q.Get("originator"))
	}
}

func TestServerIndexAndAPIKey(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())

	// Swap the probe out for a fake that always succeeds.
	s.probeFn = func(ctx context.Context, provider, key string) error { return nil }

	resp, err := http.Get(s.URL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("index status %d", resp.StatusCode)
	}

	resp, err = http.Get(s.URL() + "/apikey?provider=anthropic")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("apikey form status %d", resp.StatusCode)
	}

	// POST a fake key and expect a success result on the channel.
	_, err = http.PostForm(s.URL()+"/apikey", url.Values{
		"provider": {"anthropic"},
		"api_key":  {"sk-ant-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-s.Result():
		if r.Err != nil {
			t.Fatalf("unexpected err: %v", r.Err)
		}
		if r.Provider != "anthropic" || r.APIKey != "sk-ant-test" || r.Method != "apikey" {
			t.Fatalf("bad result: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result")
	}
}

func TestServerCallback(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())

	u := s.URL() + "/callback?code=abc123&state=anthropic:xyz"
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case r := <-s.Result():
		if r.Provider != "anthropic" || r.Code != "abc123" || r.State != "anthropic:xyz" || r.Method != "oauth" {
			t.Fatalf("bad result: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result")
	}
}

func TestCallbackServerRoundTrip(t *testing.T) {
	// Use a provider config with an ephemeral port so tests stay hermetic.
	op := OAuthProvider{
		Name:         "anthropic",
		RedirectHost: "127.0.0.1",
		RedirectPort: 0, // let kernel pick
		RedirectPath: "/callback",
	}
	// We need a real fixed port for CallbackServer.NewCallbackServer; find one.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	op.RedirectPort = port

	cs, err := NewCallbackServer(op, "thestate")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Shutdown()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(cs.URL() + "?code=abc&state=thestate")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := cs.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != "abc" || r.State != "thestate" {
		t.Fatalf("bad result: %+v", r)
	}
}

func TestCallbackServerStateMismatch(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	op := OAuthProvider{Name: "anthropic", RedirectHost: "127.0.0.1", RedirectPort: port, RedirectPath: "/callback"}
	cs, err := NewCallbackServer(op, "expected")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Shutdown()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(cs.URL() + "?code=abc&state=wrong")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := cs.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), "state") {
		t.Fatalf("want state error, got %+v", r)
	}
}

// TestStartManualOAuthAdoptsLoopbackFlow locks the fix for the Codex
// "state mismatch" bug: the interactive login starts the loopback flow
// (StartOAuth) and the paste-back flow (StartManualOAuth) back-to-back
// for the same provider. OpenAI's manual variant has no off-host page,
// so it shares the localhost:1455 callback the loopback flow already
// owns. The manual URL must therefore carry the SAME state the live
// callback server expects, otherwise opening the displayed URL redirects
// back with a state the server rejects ("state mismatch").
func TestStartManualOAuthAdoptsLoopbackFlow(t *testing.T) {
	m := NewManager(NewStore(filepath.Join(t.TempDir(), "auth.json")))
	m.openBrowser = false

	loopURL, err := m.StartOAuth("openai-codex")
	if err != nil {
		// Port 1455 is fixed by OpenAI's client registration; skip if it's
		// busy rather than fail a hermetic run.
		t.Skipf("cannot start loopback flow (port likely busy): %v", err)
	}
	defer m.CancelOAuth()

	manualURL, err := m.StartManualOAuth("openai-codex")
	if err != nil {
		t.Fatalf("StartManualOAuth: %v", err)
	}

	stateOf := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u.Query().Get("state")
	}
	loopState, manualState := stateOf(loopURL), stateOf(manualURL)
	if loopState == "" {
		t.Fatal("loopback URL has empty state")
	}
	if manualState != loopState {
		t.Fatalf("manual state %q != loopback state %q: opening the displayed URL would yield a state mismatch", manualState, loopState)
	}
}

func TestServerCallbackError(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())

	_, _ = http.Get(s.URL() + "/callback?error=access_denied&state=openai:xyz")
	select {
	case r := <-s.Result():
		if r.Err == nil || r.Provider != "openai" {
			t.Fatalf("bad result: %+v", r)
		}
		if !strings.Contains(r.Err.Error(), "access_denied") {
			t.Fatalf("missing reason: %v", r.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result")
	}
}
