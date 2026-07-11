//go:build terva_web

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// TestWSCarrierRoundTrip drives the full carrier over a real WebSocket against a
// fake service (no credentials): handshake, subscribe (snapshot), prompt, and
// the streamed events.
func TestWSCarrierRoundTrip(t *testing.T) {
	svc := newFakeWS()
	srv := httptest.NewServer(newMux(context.Background(), svc, Options{Version: "test"}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	mustWrite(t, c, ctrlproto.HelloFrame(ctrlproto.Hello{
		Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol,
		Groups: []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupSession},
	}))
	if f := mustRead(t, c); f.Kind != ctrlproto.KindHello || f.Hello == nil || f.Hello.Role != ctrlproto.RoleServer {
		t.Fatalf("want server hello, got %+v", f)
	}

	mustWrite(t, c, mustCmd(t, 1, "s1", ctrlproto.MethodSubscribe, nil))
	mustWrite(t, c, mustCmd(t, 2, "s1", ctrlproto.MethodPrompt, ctrlproto.PromptParams{Text: "hi"}))

	var gotSnapshot, gotDelta, gotEnd bool
	deadline := time.After(4 * time.Second)
	for !(gotSnapshot && gotDelta && gotEnd) {
		select {
		case <-deadline:
			t.Fatalf("timeout: snapshot=%v delta=%v end=%v", gotSnapshot, gotDelta, gotEnd)
		default:
		}
		f := mustRead(t, c)
		if f.Kind != ctrlproto.KindEvent || f.Event == nil {
			continue // resp frames etc.
		}
		switch f.Event.Type {
		case ctrlproto.EventSnapshot:
			gotSnapshot = true
		case "text_delta":
			gotDelta = true
		case "turn_end":
			gotEnd = true
		}
	}

	svc.mu.Lock()
	prompts := append([]string(nil), svc.prompts...)
	svc.mu.Unlock()
	if len(prompts) != 1 || prompts[0] != "hi" {
		t.Fatalf("prompt not delivered: %v", prompts)
	}
}

func TestAuthTokenGate(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	// No token → 401.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", resp.StatusCode)
	}

	// Correct bearer → 200.
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("with token: status %d, want 200", resp2.StatusCode)
	}

	// Wrong bearer → 401.
	req3, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req3.Header.Set("Authorization", "Bearer nope")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", resp3.StatusCode)
	}
}

func TestAuthTokenCookieBootstrap(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	// A browser presents the token in the URL once; the response hands back a
	// cookie so the asset subresources it then fetches (no header, no query)
	// authenticate — otherwise index.html loads but every /assets/* 401s.
	req, _ := http.NewRequest("GET", srv.URL+"/?token=secret", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token in query: status %d, want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "terva_token" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != "secret" {
		t.Fatalf("expected a terva_token cookie carrying the token, got %v", resp.Cookies())
	}
	if !cookie.HttpOnly {
		t.Error("token cookie must be HttpOnly")
	}

	// A follow-up carrying only that cookie — no header, no query, as a
	// fingerprinted asset fetch would — authenticates.
	req2, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("cookie-only request: status %d, want 200", resp2.StatusCode)
	}
}

func TestStaticServed(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	// Read the whole page: vite injects a variable amount of <head> markup
	// (module script, stylesheet, manifest, PWA register) ahead of the
	// <title>, so a fixed-size prefix read is brittle.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(body)), "terva") {
		t.Errorf("index.html did not mention terva: %q", body)
	}
}

// TestStaticSecurityHeaders: every static/PWA response carries the
// defense-in-depth headers (CSP, nosniff, referrer, permissions); the
// unauthenticated health check does not need them and the WS upgrade
// path must not be wrapped (a 101 ignores them anyway).
func TestStaticSecurityHeaders(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self' ws: wss:",
		"frame-ancestors 'none'",
		"object-src 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow unsafe-inline/eval: %q", csp)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if resp.Header.Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy missing")
	}

	// The SPA fallback path (unknown route -> index.html) gets them too.
	resp2, err := http.Get(srv.URL + "/some/client/route")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.Header.Get("Content-Security-Policy") == "" {
		t.Error("SPA fallback response missing CSP")
	}
}

func TestCheckBindSafety(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		opts Options
		ok   bool
	}{
		{"loopback no auth", Options{Addr: "127.0.0.1:8730"}, true},
		{"wildcard no auth", Options{Addr: ":8730"}, false},
		{"public no auth", Options{Addr: "0.0.0.0:8730"}, false},
		{"public token", Options{Addr: "0.0.0.0:8730", Token: "x"}, true},
		// Header alone no longer blesses a public bind — the header is forgeable
		// from the network without a named trusted proxy (was the HIGH finding).
		{"public header only", Options{Addr: "0.0.0.0:8730", AuthHeader: "X-User"}, false},
		{"public header + trusted proxy", Options{Addr: "0.0.0.0:8730", AuthHeader: "X-User", TrustedProxies: trusted}, true},
		{"public insecure override", Options{Addr: "0.0.0.0:8730", AllowInsecure: true}, true},
		{"public insecure-cidr scoped", Options{Addr: "0.0.0.0:8730", InsecureCIDRs: trusted}, true},
	}
	for _, c := range cases {
		err := checkBindSafety(c.opts)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected a fail-closed error", c.name)
		}
	}
}

// TestForwardAuthHeaderTrust covers the fix for the HIGH finding: the forward-
// auth header is honored only from a loopback peer or a configured trusted-proxy
// CIDR, never from an arbitrary client that could forge it.
func TestForwardAuthHeaderTrust(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{AuthHeader: "X-User", TrustedProxies: trusted}
	withHeader := func(remote string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.Header.Set("X-User", "owner")
		r.RemoteAddr = remote
		return r
	}
	if !authorized(opts, withHeader("127.0.0.1:5555")) {
		t.Error("header from loopback (same-host proxy) should be honored")
	}
	if !authorized(opts, withHeader("10.0.0.9:5555")) {
		t.Error("header from a trusted-proxy CIDR should be honored")
	}
	if authorized(opts, withHeader("192.168.1.5:5555")) {
		t.Error("header from an untrusted peer must be rejected (forgery path)")
	}
	noHeader := httptest.NewRequest("GET", "/ws", nil)
	noHeader.RemoteAddr = "127.0.0.1:5555"
	if authorized(opts, noHeader) {
		t.Error("missing header should be rejected even from loopback")
	}
}

// TestHostAllowedRebind covers the DNS-rebinding defense: in no-auth mode the
// Host must be a loopback name; an auth mode lets proxy hostnames through.
func TestHostAllowedRebind(t *testing.T) {
	mk := func(host string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.Host = host
		return r
	}
	noauth := Options{}
	if !hostAllowed(noauth, mk("127.0.0.1:8730")) || !hostAllowed(noauth, mk("localhost:8730")) {
		t.Error("loopback Host must be allowed in no-auth mode")
	}
	if hostAllowed(noauth, mk("attacker.example:8730")) {
		t.Error("non-loopback Host (DNS rebind) must be forbidden in no-auth mode")
	}
	if !hostAllowed(Options{Token: "x"}, mk("panel.example.com")) {
		t.Error("an auth mode should not restrict the Host (proxy hostnames vary)")
	}
}

// TestInsecureCIDR covers --web-insecure-cidr: no-auth access scoped to a source
// range (e.g. a tailnet), with the Host check relaxed for in-range peers but the
// loopback-source rebind defense preserved. Also checks blanket --web-insecure.
func TestInsecureCIDR(t *testing.T) {
	cidrs, err := ParseTrustedProxies([]string{"100.64.0.0/10"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := Options{InsecureCIDRs: cidrs}
	req := func(remote, host string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr = remote
		r.Host = host
		return r
	}

	// authorized: no-auth, but only loopback + the named CIDR.
	if !authorized(scoped, req("100.71.8.29:5555", "100.91.97.14:8730")) {
		t.Error("in-CIDR source should be authorized (no-auth scoped)")
	}
	if !authorized(scoped, req("127.0.0.1:5555", "127.0.0.1:8730")) {
		t.Error("loopback source should be authorized on a scoped listener")
	}
	if authorized(scoped, req("192.168.1.5:5555", "192.168.1.5:8730")) {
		t.Error("out-of-CIDR source must be rejected even though bound public")
	}

	// hostAllowed: an in-CIDR peer may use the listener's real Host (the point);
	// a loopback source still gets the loopback-Host rebind check.
	if !hostAllowed(scoped, req("100.71.8.29:5555", "100.91.97.14:8730")) {
		t.Error("in-CIDR peer must be allowed to use the listener's real Host")
	}
	if hostAllowed(scoped, req("127.0.0.1:5555", "attacker.example:8730")) {
		t.Error("loopback source must still be Host-checked on a scoped listener (rebind)")
	}
	if !hostAllowed(scoped, req("127.0.0.1:5555", "127.0.0.1:8730")) {
		t.Error("loopback source with a loopback Host must be allowed")
	}

	// Blanket --web-insecure: any source authorized, Host unrestricted.
	blanket := Options{AllowInsecure: true}
	if !authorized(blanket, req("203.0.113.7:5555", "example.com:8730")) {
		t.Error("blanket insecure authorizes any source")
	}
	if !hostAllowed(blanket, req("203.0.113.7:5555", "example.com:8730")) {
		t.Error("blanket insecure must not restrict the Host")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8730": true,
		"localhost:80":   true,
		"[::1]:80":       true,
		"0.0.0.0:80":     false,
		":80":            false,
		"192.168.1.5:80": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// --- test helpers ---

func mustCmd(t *testing.T, id uint64, sess string, m ctrlproto.Method, params any) ctrlproto.Frame {
	t.Helper()
	f, err := ctrlproto.CmdFrame(id, sess, m, params)
	if err != nil {
		t.Fatalf("CmdFrame: %v", err)
	}
	return f
}

func mustWrite(t *testing.T, c *websocket.Conn, f ctrlproto.Frame) {
	t.Helper()
	if err := c.WriteJSON(f); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustRead(t *testing.T, c *websocket.Conn) ctrlproto.Frame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f ctrlproto.Frame
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

// fakeWS is a minimal ctrlproto.WorkspaceService for carrier tests.
type fakeWS struct {
	mu      sync.Mutex
	subs    map[string][]chan ctrlproto.Event
	prompts []string
}

var _ ctrlproto.WorkspaceService = (*fakeWS)(nil)

func newFakeWS() *fakeWS { return &fakeWS{subs: map[string][]chan ctrlproto.Event{}} }

func (f *fakeWS) bcast(sess string, ev ctrlproto.Event) {
	f.mu.Lock()
	chs := append([]chan ctrlproto.Event(nil), f.subs[sess]...)
	f.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (f *fakeWS) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	ch := make(chan ctrlproto.Event, 16)
	ch <- ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Session: ctrlproto.SessionInfo{ID: sess}})
	f.mu.Lock()
	f.subs[sess] = append(f.subs[sess], ch)
	f.mu.Unlock()
	return ch, nil
}

func (f *fakeWS) Prompt(ctx context.Context, sess, text string, images []ctrlproto.Image) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	f.mu.Unlock()
	f.bcast(sess, ctrlproto.ConversationEvent(core.WireEvent{Type: "text_delta", Delta: "echo " + text}))
	f.bcast(sess, ctrlproto.ConversationEvent(core.WireEvent{Type: "turn_end", Stop: "end_turn"}))
	return nil
}

func (f *fakeWS) Queue(ctx context.Context, sess, text string) error          { return nil }
func (f *fakeWS) SetQueue(ctx context.Context, sess string, t []string) error { return nil }
func (f *fakeWS) Cancel(ctx context.Context, sess string) error               { return nil }
func (f *fakeWS) Compact(ctx context.Context, sess string) error              { return nil }
func (f *fakeWS) Clear(ctx context.Context, sess string) error                { return nil }
func (f *fakeWS) SideChatOpen(ctx context.Context, sess string) (string, error) {
	return "sc1", nil
}
func (f *fakeWS) SideChatAsk(ctx context.Context, sess, id string, prior []ctrlproto.SideChatTurn, q string) (string, error) {
	return "", nil
}
func (f *fakeWS) SideChatClose(ctx context.Context, sess, id string) error { return nil }
func (f *fakeWS) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	return nil
}
func (f *fakeWS) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error { return nil }
func (f *fakeWS) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	return []ctrlproto.SessionInfo{{ID: "s1"}}, nil
}
func (f *fakeWS) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	return ctrlproto.SessionInfo{ID: "s1"}, nil
}
func (f *fakeWS) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	return ctrlproto.SessionInfo{ID: sess}, nil
}
func (f *fakeWS) RenameSession(ctx context.Context, sess, title string) error { return nil }
func (f *fakeWS) DeleteSession(ctx context.Context, sess string) error        { return nil }
func (f *fakeWS) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	return core.WireUsage{}, nil
}
func (f *fakeWS) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	return ctrlproto.UsageInfo{}, nil
}
func (f *fakeWS) ListResets(ctx context.Context, sess string) (ctrlproto.ResetsListResult, error) {
	return ctrlproto.ResetsListResult{}, nil
}
func (f *fakeWS) ConsumeReset(ctx context.Context, sess, id string) (ctrlproto.ResetConsumeResult, error) {
	return ctrlproto.ResetConsumeResult{}, nil
}
func (f *fakeWS) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	return ctrlproto.ContextBreakdown{}, nil
}
func (f *fakeWS) Node(ctx context.Context, sess, id, op string) (ctrlproto.ContextNode, error) {
	return ctrlproto.ContextNode{ID: id}, nil
}
func (f *fakeWS) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	return nil, nil
}
func (f *fakeWS) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	return ctrlproto.Surface{}, nil
}
func (f *fakeWS) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	return nil
}
func (f *fakeWS) Catalog(ctx context.Context, lang string) (ctrlproto.CatalogView, error) {
	return ctrlproto.CatalogView{Lang: lang}, nil
}
func (f *fakeWS) Models(ctx context.Context) ([]ctrlproto.ModelInfo, error) { return nil, nil }
func (f *fakeWS) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	return nil
}
func (f *fakeWS) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	return nil
}
func (f *fakeWS) Trust(ctx context.Context, parent bool) error { return nil }
func (f *fakeWS) Untrust(ctx context.Context) error            { return nil }
func (f *fakeWS) Restart(ctx context.Context) error            { return nil }
