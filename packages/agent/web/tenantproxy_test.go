//go:build terva_web

package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
	"terva.sh/terva/packages/testsupport"
)

// fakeChild is a stand-in for a tenant's `terva web`: it serves the real
// websocket protocol on a unix socket, records every frame that reaches it, and
// answers a full server hello. What it records is the assertion that matters —
// a refused frame must not merely fail, it must never arrive.
type fakeChild struct {
	socket string

	mu   sync.Mutex
	seen []ctrlproto.Frame
}

func (f *fakeChild) received() []ctrlproto.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ctrlproto.Frame(nil), f.seen...)
}

func startFakeChild(t *testing.T) *fakeChild {
	t.Helper()
	sock := filepath.Join(testsupport.SocketDir(t), "child.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("child socket: %v", err)
	}
	fc := &fakeChild{socket: sock}

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// The child offers everything it serves, because on its own socket the
		// only caller is the supervisor.
		hello := ctrlproto.Hello{
			Role:     ctrlproto.RoleServer,
			Protocol: ctrlproto.Protocol,
			Agent:    "fake child",
			Groups: []ctrlproto.Group{
				ctrlproto.GroupConversation, ctrlproto.GroupSession,
				ctrlproto.GroupControl, ctrlproto.GroupAuth, ctrlproto.GroupSecrets,
			},
			// ...including features about ITS OWN routes, which is the honest
			// thing for a child to say and the wrong thing for a browser to
			// hear: the browser is talking to the supervisor. A tenant reaches
			// this state by turning web_stage on in their own config.
			Features: []string{ctrlproto.FeatureImages, ctrlproto.FeatureStage},
		}
		_ = c.WriteJSON(ctrlproto.Frame{Kind: ctrlproto.KindHello, Hello: &hello})
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var got ctrlproto.Frame
			if err := json.Unmarshal(raw, &got); err != nil {
				continue
			}
			fc.mu.Lock()
			fc.seen = append(fc.seen, got)
			fc.mu.Unlock()
			// Echo the raw bytes back so a fidelity test can inspect what the
			// supervisor forwards in the other direction too.
			_ = c.WriteMessage(websocket.TextMessage, raw)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); _ = ln.Close() })
	return fc
}

// proxyServer mounts proxyWS with a fixed principal, standing in for
// authMiddleware having already run.
func proxyServer(t *testing.T, fc *fakeChild, p authz.Principal, carrier tenant.Carrier) *httptest.Server {
	t.Helper()
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return &tenant.Child{ID: "t-" + strings.Repeat("a", 16), Socket: fc.socket}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
		proxyWS(r.Context(), resolve, carrier, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialProxy(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readFrame(t *testing.T, c *websocket.Conn) ctrlproto.Frame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var f ctrlproto.Frame
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

func skipIfNoUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the child carrier is a unix socket")
	}
}

// The whole point of gating at the proxy rather than in the child: a withheld
// verb must never ARRIVE. The child would have served it — on its own socket
// the only caller is the supervisor, and it trusts what reaches it.
func TestAWithheldVerbNeverReachesTheChild(t *testing.T) {
	skipIfNoUnix(t)
	fc := startFakeChild(t)
	srv := proxyServer(t, fc, authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleOwner}}, tenant.Carrier{})
	c := dialProxy(t, srv)

	readFrame(t, c) // the hello

	if err := c.WriteJSON(ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodAuthLoginStart}); err != nil {
		t.Fatal(err)
	}
	got := readFrame(t, c)
	if got.Error == nil || got.Error.Code != ctrlproto.CodeUnsupported {
		t.Fatalf("want %s, got %+v", ctrlproto.CodeUnsupported, got)
	}

	// The control: a PERMITTED verb on the same connection does arrive, so the
	// assertion below is about the gate and not about a dead socket.
	if err := c.WriteJSON(ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 2, Method: ctrlproto.MethodSessionsList}); err != nil {
		t.Fatal(err)
	}
	readFrame(t, c)

	for _, f := range fc.received() {
		if f.Method == ctrlproto.MethodAuthLoginStart {
			t.Fatal("the credential verb reached the child")
		}
	}
	if len(fc.received()) == 0 {
		t.Fatal("CONTROL: nothing reached the child at all — the proxy is not forwarding")
	}
}

// The capability axis over a real connection: a viewer holds a member's groups
// and differs only in what it may spend.
func TestAViewersPromptIsRefusedAndNeverForwarded(t *testing.T) {
	skipIfNoUnix(t)
	fc := startFakeChild(t)
	srv := proxyServer(t, fc, authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleViewer}}, tenant.Carrier{})
	c := dialProxy(t, srv)
	readFrame(t, c)

	if err := c.WriteJSON(ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodPrompt}); err != nil {
		t.Fatal(err)
	}
	got := readFrame(t, c)
	if got.Error == nil || got.Error.Code != ctrlproto.CodeForbidden {
		t.Fatalf("want %s, got %+v", ctrlproto.CodeForbidden, got)
	}
	for _, f := range fc.received() {
		if f.Method == ctrlproto.MethodPrompt {
			t.Fatal("the viewer's prompt reached the child and would have spent the subscription")
		}
	}
}

// The browser must be told what it may actually do, or the panel renders
// affordances the gate will refuse.
func TestTheBrowserSeesANarrowedHello(t *testing.T) {
	skipIfNoUnix(t)
	fc := startFakeChild(t)
	srv := proxyServer(t, fc, authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleOwner}}, tenant.Carrier{})
	c := dialProxy(t, srv)

	f := readFrame(t, c)
	if f.Kind != ctrlproto.KindHello || f.Hello == nil {
		t.Fatalf("first frame was not a hello: %+v", f)
	}
	for _, g := range f.Hello.Groups {
		if g == ctrlproto.GroupAuth || g == ctrlproto.GroupSecrets {
			t.Errorf("the hello advertises %s to a tenant", g)
		}
	}
	// The control: narrowing kept the groups a tenant does get, so this is not
	// an empty hello passing by default.
	var sawConversation bool
	for _, g := range f.Hello.Groups {
		if g == ctrlproto.GroupConversation {
			sawConversation = true
		}
	}
	if !sawConversation {
		t.Error("CONTROL: conversation was stripped too — the hello is empty, not narrowed")
	}
	// The child's other hello fields must survive the rewrite untouched.
	if f.Hello.Agent != "fake child" || f.Hello.Protocol != ctrlproto.Protocol {
		t.Errorf("narrowing damaged the hello: agent=%q protocol=%d", f.Hello.Agent, f.Hello.Protocol)
	}
}

// The claim made in proxyWS's comment: forwarded frames cross as the bytes
// their sender wrote, so a field this build does not know about survives. Had
// the proxy re-marshalled from a parsed Frame, a version skew between
// supervisor and child would silently drop data.
func TestAnUnknownFieldSurvivesTheCrossing(t *testing.T) {
	skipIfNoUnix(t)
	fc := startFakeChild(t)
	srv := proxyServer(t, fc, authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleOwner}}, tenant.Carrier{})
	c := dialProxy(t, srv)
	readFrame(t, c)

	raw := `{"kind":"cmd","id":9,"method":"sessions.list","from_a_newer_build":{"nested":42}}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	// The fake child echoes what it received, so what comes back is what
	// crossed the proxy.
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, back, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(back, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["from_a_newer_build"]; !ok {
		t.Errorf("the proxy dropped a field it did not recognise: %s", back)
	}
}

// A principal the middleware never set is the shape of a mis-wired route, and
// the safe reading of "we do not know who this is" is not "the owner".
func TestAMissingPrincipalIsRefused(t *testing.T) {
	skipIfNoUnix(t)
	fc := startFakeChild(t)
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return &tenant.Child{ID: "t-" + strings.Repeat("a", 16), Socket: fc.socket}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyWS(r.Context(), resolve, tenant.Carrier{}, w, r) // no principal in the context
	}))
	t.Cleanup(srv.Close)

	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err == nil {
		t.Fatal("a connection with no principal was proxied")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %v", resp)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// A tenant with no environment must be told over HTTP, where the status
// survives — after the upgrade a failure is a bare close the panel cannot tell
// from the machine going to sleep.
func TestNoEnvironmentIsAnHTTPStatusNotACloseFrame(t *testing.T) {
	skipIfNoUnix(t)
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return nil, tenant.ErrNotEnrolled
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := authz.Principal{Subject: "s", Source: authz.SourceOIDC, Roles: []authz.Role{authz.RoleMember}}
		r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
		proxyWS(r.Context(), resolve, tenant.Carrier{}, w, r)
	}))
	t.Cleanup(srv.Close)

	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err == nil {
		t.Fatal("an unenrolled principal was proxied")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("want 403, got %v", resp)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
