//go:build terva_web

package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/oidc"
	"terva.sh/terva/packages/agent/tenant"
	"terva.sh/terva/packages/testsupport"
)

// A supervisor that cannot tell callers apart would put several people in one
// environment, and nothing in the running system would look wrong.
func TestASupervisorRefusesToStartWithoutAWayToNamePeople(t *testing.T) {
	t.Run("nothing at all", func(t *testing.T) {
		err := CheckSupervisorAuth(Options{})
		if err == nil {
			t.Fatal("a supervisor with no auth started")
		}
		if !strings.Contains(err.Error(), "web_oidc") {
			t.Errorf("the refusal does not name a remedy: %v", err)
		}
	})

	t.Run("a bearer token is not an identity", func(t *testing.T) {
		err := CheckSupervisorAuth(Options{Token: "shared-secret"})
		if err == nil {
			t.Fatal("a token-only supervisor started — everyone holding it would share one environment")
		}
		if !strings.Contains(err.Error(), "not a person") {
			t.Errorf("the refusal does not say why a token is insufficient: %v", err)
		}
	})

	// The controls: each identity source on its own is enough.
	t.Run("forward auth is enough", func(t *testing.T) {
		if err := CheckSupervisorAuth(Options{AuthHeader: "X-Forwarded-User"}); err != nil {
			t.Fatalf("CONTROL: forward-auth must be accepted: %v", err)
		}
	})
	t.Run("oidc is enough", func(t *testing.T) {
		if err := CheckSupervisorAuth(Options{OIDCProvider: &oidc.Provider{}}); err != nil {
			t.Fatalf("CONTROL: OIDC must be accepted: %v", err)
		}
	})
	t.Run("a token alongside an identity is fine", func(t *testing.T) {
		if err := CheckSupervisorAuth(Options{Token: "t", AuthHeader: "X-Forwarded-User"}); err != nil {
			t.Fatalf("CONTROL: a token as a second factor must be allowed: %v", err)
		}
	})
}

// The routes that carry a tenant's bytes must reach the tenant's child, not the
// supervisor's own $TERVA_HOME. This is D4's correction, and it is asserted by
// counting what arrived at the child rather than by reading the mux.
func TestEveryTenantScopedRouteReachesTheChild(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	child := startRecordingChild(t, &got)
	mux := newSupervisorMux(context.Background(), fixedResolver(child), newTestPanel(t), Options{AuthHeader: "X-Forwarded-User"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Every route that resolves config.TervaHome() in the serving process.
	for _, path := range []string{"/media/cards/abc", uploadPath, sharedPath + "abc"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("X-Forwarded-User", "ada")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
	}
	if len(got) != 3 {
		t.Fatalf("%d of 3 tenant routes reached the child: %v", len(got), got)
	}
}

// ...and the supervisor's own routes must NOT be proxied: they have to work
// before a tenant exists, which is how an anonymous browser becomes someone.
func TestTheLoginSurfaceIsServedBeforeAnyTenantExists(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	// The child exists and records, so "nothing arrived" is a real observation
	// rather than the absence of anywhere to arrive. The resolver still refuses,
	// standing in for "nobody is enrolled yet".
	_ = startRecordingChild(t, &got)
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return nil, tenant.ErrNotEnrolled
	}
	mux := newSupervisorMux(context.Background(), resolve, newTestPanel(t), Options{AuthHeader: "X-Forwarded-User"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/healthz", loginPath} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode >= 500 {
			t.Errorf("%s answered %d with no tenant available", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if len(got) != 0 {
		t.Errorf("the supervisor's own routes were proxied to a tenant: %v", got)
	}
}

// The allowlist is default-deny: a route nobody named does not exist, rather
// than falling through to the supervisor's own files.
func TestAnUnnamedTenantRouteIsNotProxied(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	child := startRecordingChild(t, &got)
	mux := newSupervisorMux(context.Background(), fixedResolver(child), newTestPanel(t), Options{AuthHeader: "X-Forwarded-User"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/some-route-added-later/x", nil)
	req.Header.Set("X-Forwarded-User", "ada")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, p := range got {
		if strings.Contains(p, "some-route-added-later") {
			t.Fatal("an unnamed route was proxied to a tenant by default")
		}
	}
}

// fixedResolver hands every caller the same child — tenancy is resolved
// elsewhere, and these tests are about the mux.
func fixedResolver(c *tenant.Child) TenantResolver {
	return func(context.Context, authz.Principal) (*tenant.Child, error) { return c, nil }
}

// startRecordingChild serves a plain HTTP child on a unix socket and appends
// every path it is asked for to seen. It records rather than asserts, because
// the property under test is WHICH requests arrive, not what they answer.
func startRecordingChild(t *testing.T, seen *[]string) *tenant.Child {
	t.Helper()
	sock := filepath.Join(testsupport.SocketDir(t), "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("child socket: %v", err)
	}
	var mu sync.Mutex
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); _ = ln.Close() })
	return &tenant.Child{ID: "t-" + strings.Repeat("a", 16), Socket: sock}
}

// Both entry points diverged the first time they were written separately: one
// logged why a tenant was refused and the other threw it away, so an operator
// watching a 403 saw nothing. They share resolveForRequest now, and this pins
// that they still do — by asserting the REASON reaches the log, which is the
// property that was missing, not the status.
func TestARefusedTenantIsExplainedToTheOperator(t *testing.T) {
	skipIfNoUnix(t)

	const reason = "this containment does not separate tenants"
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return nil, errors.New(reason)
	}
	mux := newSupervisorMux(context.Background(), resolve, newTestPanel(t), Options{AuthHeader: "X-Forwarded-User"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/media/cards/x", "/ws"} {
		logged := captureStderr(t, func() {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			req.Header.Set("X-Forwarded-User", "bob")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s answered %d, want 403", path, resp.StatusCode)
			}
		})
		if !strings.Contains(logged, reason) {
			t.Errorf("%s refused without logging why; stderr was %q", path, logged)
		}
		if !strings.Contains(logged, "bob") {
			t.Errorf("%s did not log WHO was refused; stderr was %q", path, logged)
		}
	}
}

// captureStderr runs fn with os.Stderr redirected and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
