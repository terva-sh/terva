//go:build terva_web

package web

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/oidc"
)

// 🚨 The hole this whole wiring had to avoid. authorized()'s first branch grants
// the OWNER principal when no auth mode is configured, and "configured" used to
// mean Token or AuthHeader only. A daemon set up with single sign-on and nothing
// else would have been read as unauthenticated — handing every anonymous caller
// full authority while appearing, from the operator's config, to be locked down.
func TestOIDCAloneCountsAsConfiguredAuth(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	if !authConfigured(opts) {
		t.Fatal("a daemon with only single sign-on reported no auth configured")
	}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	if _, ok := authorized(opts, r); ok {
		t.Fatal("an anonymous request was authorized on an OIDC-only daemon")
	}
}

// The same predicate governs the DNS-rebinding Host check and the fail-closed
// bind check. All three must agree, or one of them treats the daemon as
// unauthenticated while the others do not.
func TestOIDCSatisfiesTheBindSafetyAndHostChecks(t *testing.T) {
	opts := Options{Addr: "0.0.0.0:8730", OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	if err := checkBindSafety(opts); err != nil {
		t.Errorf("a non-loopback bind with single sign-on was refused: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "terva.example.com"
	if !hostAllowed(opts, r) {
		t.Error("a proxy hostname was refused on a daemon with single sign-on configured")
	}
}

func TestSessionCookieAuthenticatesAsItsPrincipal(t *testing.T) {
	st := NewOIDCState()
	want := authz.Principal{Subject: "u1", Display: "Drew", Roles: []authz.Role{authz.RoleMember}, Source: authz.SourceOIDC}
	st.putSession("sid-1", want, time.Hour)
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: st}

	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: "sid-1"})
	got, ok := authorized(opts, r)
	if !ok {
		t.Fatal("a valid session cookie did not authenticate")
	}
	if got.Subject != "u1" || got.Source != authz.SourceOIDC {
		t.Errorf("principal = %+v", got)
	}
	if !got.Has(authz.RoleMember) {
		t.Errorf("roles lost: %+v", got.Roles)
	}
}

// An unknown session id must not authenticate, and must not be treated as
// "no cookie, fall through to owner" either.
func TestUnknownSessionCookieDoesNotAuthenticate(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: "made-up"})
	if _, ok := authorized(opts, r); ok {
		t.Fatal("an unknown session id authenticated")
	}
}

func TestExpiredSessionDoesNotAuthenticate(t *testing.T) {
	st := NewOIDCState()
	st.putSession("sid-x", authz.Principal{Subject: "u", Roles: []authz.Role{authz.RoleOwner}}, -time.Second)
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: st}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: "sid-x"})
	if _, ok := authorized(opts, r); ok {
		t.Fatal("an expired session authenticated")
	}
}

// 🚨 Logout must drop the RECORD, not merely the cookie. Clearing only the
// cookie leaves a live session id that anyone holding a copy keeps using.
func TestLogoutInvalidatesTheSessionServerSide(t *testing.T) {
	st := NewOIDCState()
	st.putSession("sid-2", authz.Principal{Subject: "u", Roles: []authz.Role{authz.RoleOwner}}, time.Hour)
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: st}

	r := httptest.NewRequest(http.MethodGet, oidcLogoutPath, nil)
	r.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: "sid-2"})
	handleOIDCLogout(opts)(httptest.NewRecorder(), r)

	if _, ok := st.session("sid-2"); ok {
		t.Fatal("the session survived logout server-side; clearing the cookie is not enough")
	}
}

// A login attempt is single-use, so a replayed callback finds nothing.
func TestAttemptIsSingleUse(t *testing.T) {
	st := NewOIDCState()
	st.putAttempt("a1", oidcAttempt{state: "s", expires: time.Now().Add(time.Minute)})
	if _, ok := st.takeAttempt("a1"); !ok {
		t.Fatal("the first take failed")
	}
	if _, ok := st.takeAttempt("a1"); ok {
		t.Fatal("an attempt was consumable twice — a replayed callback would succeed")
	}
}

func TestExpiredAttemptIsRefused(t *testing.T) {
	st := NewOIDCState()
	st.putAttempt("a2", oidcAttempt{state: "s", expires: time.Now().Add(-time.Second)})
	if _, ok := st.takeAttempt("a2"); ok {
		t.Fatal("an expired attempt was accepted")
	}
}

// 🚨 The CSRF check. Without it, an attacker who makes this browser GET the
// callback with THEIR authorization code logs the victim into the attacker's
// account, and everything typed afterwards lands in a session they control.
func TestCallbackRefusesAStateMismatch(t *testing.T) {
	st := NewOIDCState()
	st.putAttempt("a3", oidcAttempt{state: "the-real-state", next: "/", expires: time.Now().Add(time.Minute)})
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: st}

	r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?code=c&state=attacker-state", nil)
	r.AddCookie(&http.Cookie{Name: oidcAttemptCookie, Value: "a3"})
	w := httptest.NewRecorder()
	handleOIDCCallback(opts)(w, r)

	if w.Code == http.StatusSeeOther {
		t.Fatal("a callback with a mismatched state completed a login")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcSessionCookie && c.Value != "" {
			t.Fatal("a session cookie was issued despite the state mismatch")
		}
	}
}

// A callback with no in-flight attempt for this browser must not log anyone in.
func TestCallbackWithoutAnAttemptCookieFails(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	r := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?code=c&state=s", nil)
	w := httptest.NewRecorder()
	handleOIDCCallback(opts)(w, r)
	if w.Code == http.StatusSeeOther {
		t.Fatal("a callback with no attempt cookie completed a login")
	}
}

// The routes exist whether or not a provider is configured, and answer 404 when
// it is not — a known path with a clear answer beats a route that vanishes.
func TestOIDCRoutesAnswer404WhenUnconfigured(t *testing.T) {
	opts := Options{}
	for _, path := range []string{oidcStartPath, oidcCallbackPath} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		switch path {
		case oidcStartPath:
			handleOIDCStart(opts)(w, r)
		default:
			handleOIDCCallback(opts)(w, r)
		}
		if w.Code != http.StatusNotFound {
			t.Errorf("%s answered %d with no provider configured, want 404", path, w.Code)
		}
	}
}

// 🔑 A role name the build does not know must be DROPPED, not passed through.
// authz.Grant ignores it either way — but a string that travels makes a typo in
// an operator's role map look like a working grant.
func TestUnknownRoleNamesAreDropped(t *testing.T) {
	got := authzRoles([]string{"owner", "wizard", "member"})
	if len(got) != 2 {
		t.Fatalf("roles = %v, want the two known ones", got)
	}
	for _, r := range got {
		if r == authz.Role("wizard") {
			t.Error("an unknown role name survived")
		}
	}
}

// safeNext already defends the redirect; this pins that the login flow uses it,
// since an open redirect on a login callback is how a phish gets its landing.
func TestNextIsConstrainedToThisSite(t *testing.T) {
	for _, bad := range []string{"//evil.example.com", "https://evil.example.com", "/\r\nX", ""} {
		if got := safeNext(bad); !strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") {
			t.Errorf("safeNext(%q) = %q, which leaves this site", bad, got)
		}
	}
}

// signingIdP is a minimal OpenID provider that really signs, so this exercises
// the shipped verification path rather than a stub.
type signingIdP struct {
	srv    *httptest.Server
	issuer string
	key    *rsa.PrivateKey
	groups []string
	nonce  string
}

func newSigningIdP(t *testing.T) *signingIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	f := &signingIdP{key: key, groups: []string{"terva-users"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize",
			"token_endpoint": f.issuer + "/token", "jwks_uri": f.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		// x/oauth2 parses by Content-Type; without this it reads the body as
		// form-encoded and reports a missing access_token.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer",
			"id_token": f.mint(t, r.PostForm.Get("nonce")),
		})
	})
	f.srv = httptest.NewTLSServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// nonce is threaded in by the test, since the IdP would normally echo the one
// from the authorize request.
func (f *signingIdP) mint(t *testing.T, nonce string) string {
	t.Helper()
	payload := map[string]any{
		"iss": f.issuer, "sub": "user-abc", "aud": "terva",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"name": "Drew", "email": "drew@example.com",
		"groups": f.groups, "nonce": f.nonce,
	}
	b, _ := json.Marshal(payload)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	obj, err := signer.Sign(b)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, _ := obj.CompactSerialize()
	return raw
}

// 🚨 The end-to-end proof that 3b works: a browser walks start → provider →
// callback and comes out authenticated as a principal carrying the role the
// operator's group map grants. Everything below it in this file tests a piece;
// this tests that the pieces are connected.
func TestFullLoginEstablishesAPrincipalWithMappedRoles(t *testing.T) {
	idp := newSigningIdP(t)
	prov, err := oidc.Discover(t.Context(), oidc.Config{
		Issuer: idp.issuer, ClientID: "terva",
		RoleMap: map[string]string{"terva-users": "member"},
	}, "https://terva.example/auth/oidc/callback", idp.srv.Client())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	st := NewOIDCState()
	opts := Options{OIDCProvider: prov, OIDCState: st, OIDCHTTPClient: idp.srv.Client()}

	// 1. Start: expect a redirect to the IdP and an attempt cookie.
	w := httptest.NewRecorder()
	handleOIDCStart(opts)(w, httptest.NewRequest(http.MethodGet, oidcStartPath+"?next=/panel", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start answered %d, want a redirect", w.Code)
	}
	var attemptCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcAttemptCookie {
			attemptCookie = c
		}
	}
	if attemptCookie == nil {
		t.Fatal("start issued no attempt cookie")
	}
	// 🔑 Lax is required: the callback is a cross-site top-level navigation, so
	// a Strict cookie would never be sent and every login would fail.
	if attemptCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("attempt cookie SameSite = %v, want Lax", attemptCookie.SameSite)
	}
	loc, err := url.Parse(w.Result().Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	state := loc.Query().Get("state")
	idp.nonce = loc.Query().Get("nonce") // the IdP echoes what it was sent
	if state == "" || idp.nonce == "" {
		t.Fatalf("authorize URL missing state/nonce: %s", loc)
	}

	// 2. Callback, carrying the attempt cookie and the state we were given.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
	r2.AddCookie(attemptCookie)
	handleOIDCCallback(opts)(w2, r2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("callback answered %d, want a redirect (body: %s)", w2.Code, w2.Body.String())
	}
	if got := w2.Result().Header.Get("Location"); got != "/panel" {
		t.Errorf("landed at %q, want the ?next= target", got)
	}
	var sess *http.Cookie
	for _, c := range w2.Result().Cookies() {
		if c.Name == oidcSessionCookie && c.Value != "" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("callback issued no session cookie")
	}
	if sess.SameSite != http.SameSiteStrictMode || !sess.HttpOnly {
		t.Errorf("session cookie = SameSite %v HttpOnly %v, want Strict + HttpOnly", sess.SameSite, sess.HttpOnly)
	}

	// 3. That cookie now authenticates, as a MEMBER — not the owner.
	r3 := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r3.AddCookie(sess)
	p, ok := authorized(opts, r3)
	if !ok {
		t.Fatal("the session cookie did not authenticate")
	}
	if p.Subject != "user-abc" || p.Source != authz.SourceOIDC {
		t.Errorf("principal = %+v", p)
	}
	if !p.Has(authz.RoleMember) {
		t.Fatalf("roles = %v, want member from the group map", p.Roles)
	}
	if p.Has(authz.RoleOwner) {
		t.Fatal("a mapped member came out as owner")
	}
}

// 🔑 Authenticated and deliberately not admitted: a real user whose groups map
// to nothing must NOT get a session. This is both the common misconfiguration
// and the right answer for a stranger your IdP happens to know.
func TestLoginWithNoMappedGroupIsRefusedASession(t *testing.T) {
	idp := newSigningIdP(t)
	idp.groups = []string{"some-other-team"}
	prov, err := oidc.Discover(t.Context(), oidc.Config{
		Issuer: idp.issuer, ClientID: "terva",
		RoleMap: map[string]string{"terva-users": "member"},
	}, "https://terva.example/auth/oidc/callback", idp.srv.Client())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	st := NewOIDCState()
	opts := Options{OIDCProvider: prov, OIDCState: st, OIDCHTTPClient: idp.srv.Client()}

	w := httptest.NewRecorder()
	handleOIDCStart(opts)(w, httptest.NewRequest(http.MethodGet, oidcStartPath, nil))
	var ac *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcAttemptCookie {
			ac = c
		}
	}
	loc, _ := url.Parse(w.Result().Header.Get("Location"))
	idp.nonce = loc.Query().Get("nonce")

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, oidcCallbackPath+"?code=abc&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	r2.AddCookie(ac)
	handleOIDCCallback(opts)(w2, r2)

	if w2.Code == http.StatusSeeOther {
		t.Fatal("an unmapped user was logged in")
	}
	for _, c := range w2.Result().Cookies() {
		if c.Name == oidcSessionCookie && c.Value != "" {
			t.Fatal("an unmapped user was given a session cookie")
		}
	}
}

// 🚨 An OIDC-only daemon must have a login page. Three separate places answered
// "is there anything to offer?" with `opts.Token != ""`, so before this a daemon
// with single sign-on and no bearer token rendered a 404 at /auth and a bare
// "unauthorized" on a 401 — which is exactly where the browser gets sent.
func TestOIDCOnlyDaemonStillServesALoginPage(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}

	r := httptest.NewRequest(http.MethodGet, loginPath, nil)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	if !wantsLoginPage(opts, r) {
		t.Error("a navigating browser was not offered the login page on an OIDC-only daemon")
	}

	w := httptest.NewRecorder()
	handleLogin(opts)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s answered %d on an OIDC-only daemon, want 200", loginPath, w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, oidcStartPath) {
		t.Error("the page offers no way to start a single sign-on")
	}
	// 🔑 No token field: asking for a secret that cannot exist on this daemon
	// sends the visitor hunting for something nobody has.
	if strings.Contains(body, `name="token"`) {
		t.Error("a token field was rendered on a daemon with no token configured")
	}
}

// ...and a POST still has nothing to compare against, so it stays a 404.
func TestOIDCOnlyDaemonRefusesATokenPost(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader("token=guess"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleLogin(opts)(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST answered %d, want 404 — there is no token to check against", w.Code)
	}
}

// A token-only daemon is unchanged: form, no single sign-on button.
func TestTokenOnlyDaemonPageIsUnchanged(t *testing.T) {
	opts := Options{Token: "s3cret"}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, loginPath, nil)
	handleLogin(opts)(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `name="token"`) {
		t.Error("the token field went missing on a token-only daemon")
	}
	if strings.Contains(body, oidcStartPath) {
		t.Error("a single sign-on button appeared with no provider configured")
	}
}

// Both configured: both ways in, and the deep link the visitor was denied
// survives the trip through the provider.
func TestBothModesOfferBothAndCarryNext(t *testing.T) {
	opts := Options{Token: "s3cret", OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, loginPath+"?next=%2Fpanel%2Fdeep", nil)
	handleLogin(opts)(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `name="token"`) || !strings.Contains(body, oidcStartPath) {
		t.Fatal("a daemon with both modes did not offer both")
	}
	if !strings.Contains(body, "next=%2Fpanel%2Fdeep") {
		t.Errorf("the single sign-on link dropped ?next=; body: %s", body)
	}
}

// 🚨 The login page must not become an open redirect via the SSO link. safeNext
// constrains it, and this pins that the LINK is built from the constrained value
// rather than the raw query.
func TestSSOLinkCannotCarryAnOffsiteNext(t *testing.T) {
	opts := Options{OIDCProvider: &oidc.Provider{}, OIDCState: NewOIDCState()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, loginPath+"?next=https%3A%2F%2Fevil.example.com", nil)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	handleLogin(opts)(w, r)
	if strings.Contains(w.Body.String(), "evil.example.com") {
		t.Fatal("an off-site next reached the single sign-on link")
	}
}
