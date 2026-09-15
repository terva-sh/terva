//go:build terva_web

package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/authz"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

// The forward-auth header's VALUE used to reach exactly one place in the tree —
// clientDesc, a log line — so a proxy asserted an identity terva discarded.
// It is now the principal's subject, which is what any per-user authority has
// to be keyed on.
func TestForwardAuthIdentityBecomesTheSubject(t *testing.T) {
	opts := Options{AuthHeader: "X-Forwarded-User", TrustedProxies: []*net.IPNet{cidr(t, "10.0.0.0/8")}}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = "10.0.0.9:5555"
	r.Header.Set("X-Forwarded-User", "drew@example.com")

	p, ok := authorized(opts, r)
	if !ok {
		t.Fatal("a proxied request with the header was refused")
	}
	if p.Subject != "drew@example.com" {
		t.Errorf("Subject = %q, want the asserted identity", p.Subject)
	}
	if p.Source != authz.SourceForwardAuth {
		t.Errorf("Source = %q, want %q", p.Source, authz.SourceForwardAuth)
	}
}

// An untrusted peer sending the header is still refused, and must not yield a
// principal — the header is forgeable by anyone who can reach the port, so the
// proxy check is the whole boundary.
func TestForgedForwardAuthHeaderYieldsNoPrincipal(t *testing.T) {
	opts := Options{AuthHeader: "X-Forwarded-User", TrustedProxies: []*net.IPNet{cidr(t, "10.0.0.0/8")}}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = "203.0.113.7:5555" // not the proxy
	r.Header.Set("X-Forwarded-User", "attacker")

	p, ok := authorized(opts, r)
	if ok {
		t.Fatal("a forged header from an untrusted peer was accepted")
	}
	if p.Subject != "" {
		t.Errorf("a refused request produced Subject %q; it must produce nothing", p.Subject)
	}
}

// The middleware is the only place that authenticates, so it is the only place
// that may attach a principal. A handler behind it must find one.
func TestMiddlewareHandsThePrincipalToTheHandler(t *testing.T) {
	opts := Options{Token: "s3cret"}
	var got authz.Principal
	var found bool
	h := authMiddleware(opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, found = PrincipalFrom(r)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(httptest.NewRecorder(), r)

	if !found {
		t.Fatal("the handler found no principal behind authMiddleware")
	}
	if got.Source != authz.SourceToken {
		t.Errorf("Source = %q, want %q", got.Source, authz.SourceToken)
	}
	if !got.Has(authz.RoleOwner) {
		t.Error("a bearer-token caller must still be the owner — this is what keeps the change a no-op for existing deployments")
	}
}

// 🚨 A handler mounted WITHOUT the middleware must find nothing, and the zero
// principal must grant nothing. This is the fail-closed direction: serveWS
// hands whatever it finds to authz.Restrict, so if an unauthenticated context
// yielded an owner, forgetting the middleware on a route would serve the full
// surface to anyone.
func TestHandlerWithoutMiddlewareFindsNoPrincipal(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	p, ok := PrincipalFrom(r)
	if ok {
		t.Fatal("a request that never passed authMiddleware reported a principal")
	}
	if len(authz.Grant(p)) != 0 {
		t.Fatalf("the unauthenticated principal grants %v, want nothing at all", authz.Grant(p))
	}
}

// 🚨 ServeConn's authority default is PERMISSIVE — a carrier that says nothing
// gets the full capability set, which is what every carrier did before roles
// existed. That default is right for in-process and test carriers and wrong for
// this one, which serves a network listener. So the production call must pass
// WithAuthority, and dropping it would silently restore owner authority to every
// caller while every other test still passed.
//
// Checked at the source, because the thing being asserted is that a particular
// call site keeps a particular argument — there is no runtime observation that
// distinguishes "authority happened to be capAll" from "authority was never set".
func TestWebCarrierPassesAuthority(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("read web.go: %v", err)
	}
	text := string(src)
	if !strings.Contains(text, "ctrlproto.ServeConn(") {
		t.Fatal("no ServeConn call found in web.go; this guard is watching the wrong file")
	}
	if !strings.Contains(text, "ctrlproto.WithAuthority(authz.Authority(principal))") {
		t.Error("the /ws carrier no longer passes WithAuthority(authz.Authority(principal)) — every caller would silently regain full authority")
	}
}
