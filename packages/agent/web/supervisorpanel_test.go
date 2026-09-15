//go:build terva_web

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
	"terva.sh/terva/packages/testsupport"
)

// newTestPanel builds the real producer over a real registry and supervisor.
// A double would let the page render a shape the panel never emits.
func newTestPanel(t *testing.T) *tenant.Panel {
	t.Helper()
	dir := testsupport.TempDir(t)
	store := tenant.NewStoreIn(dir)
	sup, err := tenant.NewSupervisor(tenant.SupervisorOptions{
		Root:        dir + "/tenants",
		RunDir:      testsupport.SocketDir(t),
		Containment: tenant.SameUser{},
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	t.Cleanup(sup.StopAll)
	return tenant.NewPanel(store, sup)
}

// enrolInPanel puts a person in the registry the panel reads.
func enrolInPanel(t *testing.T, dir, subject, display string) tenant.Record {
	t.Helper()
	rec, _, err := tenant.NewStoreIn(dir).Enrol(subject, display)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return rec
}

// panelServer stands up a supervisor mux whose registry the test can write to,
// returning the server and the registry's home.
func panelServer(t *testing.T, opts Options) (*httptest.Server, string, *tenant.Panel) {
	t.Helper()
	dir := testsupport.TempDir(t)
	store := tenant.NewStoreIn(dir)
	sup, err := tenant.NewSupervisor(tenant.SupervisorOptions{
		Root:        dir + "/tenants",
		RunDir:      testsupport.SocketDir(t),
		Containment: tenant.SameUser{},
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	t.Cleanup(sup.StopAll)
	panel := tenant.NewPanel(store, sup)
	resolve := func(context.Context, authz.Principal) (*tenant.Child, error) {
		return nil, tenant.ErrNotEnrolled
	}
	srv := httptest.NewServer(newSupervisorMux(context.Background(), resolve, panel, opts))
	t.Cleanup(srv.Close)
	return srv, dir, panel
}

// forwardAuthOpts authenticates by header, the mode a test can drive without an
// identity provider. The principal it produces is an OWNER.
func forwardAuthOpts() Options { return Options{AuthHeader: "X-Forwarded-User"} }

func get(t *testing.T, srv *httptest.Server, path, who string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if who != "" {
		req.Header.Set("X-Forwarded-User", who)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

// The page is the operator's, and it shows what the producer knows: who is
// enrolled, what state they are in, and what this host actually separates.
func TestTheOperatorPageShowsEveryEnvironment(t *testing.T) {
	skipIfNoUnix(t)
	srv, dir, _ := panelServer(t, forwardAuthOpts())
	ada := enrolInPanel(t, dir, "sub-ada", "Ada Lovelace")

	resp, body := get(t, srv, supervisorPath, "operator")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the panel answered %d", resp.StatusCode)
	}
	for _, want := range []string{"Ada Lovelace", "sub-ada", ada.ID, "stopped"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}
	// The containment banner: SameUser does not isolate, and a page listing
	// environments without saying so would read as several live ones.
	if !strings.Contains(body, "This host carries one tenant") {
		t.Error("the page does not warn that this host separates nobody")
	}
}

// 🚨 The gate. Managing other people's environments is the owner's, and the
// check reads the authority table rather than a role name spelled here.
func TestOnlyAPrincipalGrantedTheTenantsGroupReachesThePanel(t *testing.T) {
	skipIfNoUnix(t)
	srv, _, _ := panelServer(t, forwardAuthOpts())

	t.Run("an authenticated owner gets in", func(t *testing.T) {
		resp, _ := get(t, srv, supervisorPath, "operator")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONTROL: an owner was refused the panel (%d)", resp.StatusCode)
		}
	})

	t.Run("nobody at all is refused", func(t *testing.T) {
		// No header: authMiddleware never establishes a principal.
		resp, _ := get(t, srv, supervisorPath, "")
		if resp.StatusCode == http.StatusOK {
			t.Fatal("an unauthenticated caller reached the supervisor panel")
		}
	})

	// A principal who authenticated and holds a lesser role — the shape an OIDC
	// claim mapping produces for an ordinary user, and the case forward-auth
	// cannot construct because every header principal is an owner. Driven
	// through the middleware itself rather than asserted about the table, so
	// deleting the check fails this.
	t.Run("a member is refused, and told what is missing", func(t *testing.T) {
		for _, role := range []authz.Role{authz.RoleMember, authz.RoleOperator, authz.RoleViewer} {
			rec := httptest.NewRecorder()
			reached := false
			h := requireTenantsGrant(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			h.ServeHTTP(rec, withPrincipal(authz.Principal{Subject: "ada", Roles: []authz.Role{role}}))
			if reached {
				t.Fatalf("a %s reached the supervisor panel", role)
			}
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s was refused with %d, want 403", role, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "owner") {
				t.Errorf("%s's refusal does not name the role that would work: %q", role, rec.Body.String())
			}
		}
	})

	t.Run("CONTROL: an owner passes the same middleware", func(t *testing.T) {
		rec := httptest.NewRecorder()
		reached := false
		h := requireTenantsGrant(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		h.ServeHTTP(rec, withPrincipal(authz.Principal{Subject: "ada", Roles: []authz.Role{authz.RoleOwner}}))
		if !reached {
			t.Fatalf("CONTROL: an owner was refused (%d)", rec.Code)
		}
	})

	// And a handler reached with NO principal at all — the shape a mis-wired
	// route has — must refuse rather than assume the owner.
	t.Run("no principal is not the owner", func(t *testing.T) {
		rec := httptest.NewRecorder()
		reached := false
		h := requireTenantsGrant(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, supervisorPath, nil))
		if reached || rec.Code != http.StatusUnauthorized {
			t.Fatalf("a request with no principal reached the panel (reached=%v code=%d)", reached, rec.Code)
		}
	})
}

// withPrincipal builds a request carrying p, the way authMiddleware would.
func withPrincipal(p authz.Principal) *http.Request {
	r := httptest.NewRequest(http.MethodGet, supervisorPath, nil)
	return r.WithContext(context.WithValue(r.Context(), principalKey, p))
}

// The panel's own routes must never be proxied to a tenant, whatever a tenant
// learns about their names.
func TestThePanelRoutesAreNotProxied(t *testing.T) {
	skipIfNoUnix(t)

	var got []string
	child := startRecordingChild(t, &got)
	panel := newTestPanel(t)
	srv := httptest.NewServer(newSupervisorMux(context.Background(), fixedResolver(child), panel, forwardAuthOpts()))
	t.Cleanup(srv.Close)

	for _, p := range []string{supervisorPath, supervisorWSPath} {
		_, _ = get(t, srv, p, "operator")
	}
	for _, p := range got {
		if strings.Contains(p, "supervisor") {
			t.Fatalf("the supervisor's own route %q was proxied to a tenant", p)
		}
	}
}

// Suspending from the page reaches the registry, and answers with a redirect so
// a reload does not fire it again.
func TestSuspendingFromThePageReachesTheRegistry(t *testing.T) {
	skipIfNoUnix(t)
	srv, dir, panel := panelServer(t, forwardAuthOpts())
	ada := enrolInPanel(t, dir, "sub-ada", "Ada")

	// The client must not follow the redirect, or the assertion is about the
	// page it lands on rather than the answer to the POST.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{"id": {ada.ID}, "do": {"suspend"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+supervisorPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-User", "operator")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the action answered %d, want a 303 so a reload does not repeat it", resp.StatusCode)
	}

	list, err := panel.TenantsList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tenants) != 1 || !list.Tenants[0].Suspended {
		t.Fatalf("the registry was not updated: %+v", list.Tenants)
	}
}

// An action that FAILED must not be logged as though it worked. The first cut
// announced "drew suspended t-…" before calling the verb, so a failure wrote a
// reassuring line and a contradicting one, in that order — which is the order an
// operator scanning a log reads them in.
func TestAFailedActionIsNotLoggedAsSuccess(t *testing.T) {
	skipIfNoUnix(t)
	srv, _, _ := panelServer(t, forwardAuthOpts())

	logged := captureStderr(t, func() {
		form := url.Values{"id": {"t-0000000000000000"}, "do": {"suspend"}} // enrolled by nobody
		req, _ := http.NewRequest(http.MethodPost, srv.URL+supervisorPath, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-User", "operator")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	})
	if strings.Contains(logged, "operator suspended") {
		t.Errorf("a failed suspension was logged as done: %q", logged)
	}
	if !strings.Contains(logged, "could not suspend") {
		t.Errorf("the failure was not logged at all: %q", logged)
	}
}

// CONTROL for the test above: a suspension that WORKS is still logged, so the
// assertion there is about the failure path and not about the logging being
// gone.
func TestASuccessfulActionIsLogged(t *testing.T) {
	skipIfNoUnix(t)
	srv, dir, _ := panelServer(t, forwardAuthOpts())
	ada := enrolInPanel(t, dir, "sub-ada", "Ada")

	logged := captureStderr(t, func() {
		form := url.Values{"id": {ada.ID}, "do": {"suspend"}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+supervisorPath, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-User", "operator")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	})
	if !strings.Contains(logged, "operator suspended") || !strings.Contains(logged, ada.ID) {
		t.Errorf("a suspension was not logged with who and what: %q", logged)
	}
}

// 🚨 A page on another site must not be able to post a suspension. The session
// cookie is SameSite=Strict, but the forward-auth mode has no terva cookie at
// all and its credential is ambient — so the origin check is the one that has to
// hold for that mode.
func TestACrossOriginSuspendIsRefused(t *testing.T) {
	skipIfNoUnix(t)
	srv, dir, panel := panelServer(t, forwardAuthOpts())
	ada := enrolInPanel(t, dir, "sub-ada", "Ada")

	form := url.Values{"id": {ada.ID}, "do": {"suspend"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+supervisorPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-User", "operator")
	req.Header.Set("Origin", "https://someone-elses-site.example")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin suspend answered %d", resp.StatusCode)
	}
	list, _ := panel.TenantsList(context.Background())
	if list.Tenants[0].Suspended {
		t.Fatal("a cross-origin request suspended an environment")
	}
}

// The operator socket speaks the same protocol, advertising the one group it
// serves — this is what `terva attach` will find (step 10).
func TestTheOperatorSocketServesTheTenantsGroup(t *testing.T) {
	skipIfNoUnix(t)
	srv, dir, _ := panelServer(t, forwardAuthOpts())
	enrolInPanel(t, dir, "sub-ada", "Ada")

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + supervisorWSPath
	c, _, err := (&websocket.Dialer{}).Dial(wsURL, http.Header{"X-Forwarded-User": {"operator"}})
	if err != nil {
		t.Fatalf("dial the operator socket: %v", err)
	}
	defer c.Close()

	if err := c.WriteJSON(ctrlproto.HelloFrame(ctrlproto.Hello{
		Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol,
		Groups: []ctrlproto.Group{ctrlproto.GroupTenants},
	})); err != nil {
		t.Fatal(err)
	}
	var hello ctrlproto.Frame
	if err := c.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.Hello == nil || len(hello.Hello.Groups) != 1 || hello.Hello.Groups[0] != ctrlproto.GroupTenants {
		t.Fatalf("the operator socket advertised %+v", hello.Hello)
	}

	if err := c.WriteJSON(ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodTenantsList}); err != nil {
		t.Fatal(err)
	}
	var resp ctrlproto.Frame
	if err := c.ReadJSON(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("tenants.list over the operator socket: %v", resp.Error)
	}
	var list ctrlproto.TenantsListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tenants) != 1 {
		t.Fatalf("the socket returned %d environments", len(list.Tenants))
	}
}

// 🚨 The boundary from the TENANT's side, which is the direction that matters:
// the group is not on the proxy's allowlist, so a tenant's connection cannot
// carry it — and the daemon it would reach has no handler for it either.
func TestATenantsConnectionCannotCarryTheTenantsGroup(t *testing.T) {
	owner := authz.Principal{Subject: "ada", Roles: []authz.Role{authz.RoleOwner}}

	if tenant.Forwardable(ctrlproto.GroupTenants) {
		t.Fatal("the tenants group is forwardable across the tenant proxy")
	}
	// Even for an OWNER, who by the authority table holds the group: the
	// allowlist narrows first, so no role can widen what the proxy carries.
	offered := []ctrlproto.Group{ctrlproto.GroupConversation, ctrlproto.GroupTenants}
	for _, g := range tenant.ForwardableGroups(offered, owner) {
		if g == ctrlproto.GroupTenants {
			t.Fatal("an owner's proxied connection was offered the tenants group")
		}
	}
	// And a frame naming the verb is refused with the shape a nonexistent verb
	// gets, not one that confirms a supervisor-only surface is there.
	f := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 1, Method: ctrlproto.MethodTenantsList}
	errFrame := tenant.CheckCommand(f, owner)
	if errFrame == nil {
		t.Fatal("tenants.list was forwarded to a tenant's daemon")
	}
	if errFrame.Error.Code != ctrlproto.CodeUnsupported {
		t.Errorf("refused with %q; a boundary built on non-existence must not answer forbidden", errFrame.Error.Code)
	}
	// CONTROL: the same principal's ordinary traffic still crosses, so the
	// refusal above is about the group and not about the gate being shut.
	if tenant.CheckCommand(ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: 2, Method: ctrlproto.MethodSessionsList}, owner) != nil {
		t.Fatal("CONTROL: an owner was refused sessions.list across the proxy")
	}
}
