//go:build terva_web

package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// browserGet is a page navigation, the way a browser announces one.
func browserGet(srv *httptest.Server, path string) (*http.Response, string) {
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,*/*")
	resp, err := srv.Client().Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func loginServer(t *testing.T) *httptest.Server {
	t.Helper()
	// The throttle's buckets are a package-level map, so without this a test that
	// spends the loopback bucket on bad guesses silently 429s every later test
	// that logs in from loopback — and the failure reads as "a correct token set
	// no cookie", which points nowhere near the cause.
	loginFailures.Clear()
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "s3cret"}))
	t.Cleanup(srv.Close)
	// Don't follow the post-login redirect; the test wants to see it.
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return srv
}

// A browser with no cookie used to be told the word "unauthorized" and left
// there — the token could only ever get in through the address bar. Now the
// token has somewhere to go that no proxy log, browser history, or Referer
// header records.
func TestBrowserGetsTheFormNotTheWordUnauthorized(t *testing.T) {
	srv := loginServer(t)
	resp, body := browserGet(srv, "/")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 (the form is still a refusal)", resp.StatusCode)
	}
	if !strings.Contains(body, `name="token"`) {
		t.Fatalf("no token form in the response: %.120q", body)
	}
	if strings.Contains(body, "s3cret") {
		t.Error("the login page echoed the token back to an unauthenticated client")
	}
	// The one <style> block has to survive the page's own CSP.
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("CSP would block the form's own POST: %q", csp)
	}
	if !strings.Contains(csp, "nonce-") || !strings.Contains(body, "nonce=") {
		t.Errorf("inline style is not nonced; the page would render unstyled: %q", csp)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the login page must not be cached")
	}
}

// Everything that is not a browser navigating keeps the plain 401 and its
// WWW-Authenticate header: curl, a script's fetch(), native clients. Handing an
// HTML form to a JSON client would be a regression dressed as a feature.
//
// The subresource case used to be spelled /assets/app.js. It cannot be any more —
// the fingerprinted bundle is served OUTSIDE the gate now, because the service
// worker precaches it and a precache entry the client cannot fetch is a worker that
// cannot install (see TestThePrecacheManifestIsFetchableWithoutACredential). So the
// example moved to a path that is still gated. The rule under test is unchanged:
// answer a NAVIGATION with the form, answer everything else with a status.
func TestNonBrowserClientsStillGetAPlain401(t *testing.T) {
	srv := loginServer(t)
	for name, mk := range map[string]func() *http.Request{
		"curl": func() *http.Request {
			r, _ := http.NewRequest("GET", srv.URL+"/", nil)
			return r
		},
		"a script fetching the gated shell": func() *http.Request {
			r, _ := http.NewRequest("GET", srv.URL+"/index.html", nil)
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Accept", "*/*")
			return r
		},
	} {
		resp, err := srv.Client().Do(mk())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		if strings.Contains(string(body), "<form") {
			t.Errorf("%s: got the HTML login form instead of a plain 401", name)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: lost the WWW-Authenticate header", name)
		}
	}
}

// The round trip: post the token, get the cookie, and the cookie alone carries
// the app AND the WebSocket handshake — which is the whole point, because the
// browser cannot set a header on a WebSocket and would otherwise be back to
// ?token= in the URL.
func TestPostedTokenSetsTheCookieAndCarriesTheSocket(t *testing.T) {
	srv := loginServer(t)

	resp, err := srv.Client().PostForm(srv.URL+loginPath, url.Values{
		"token": {"s3cret"}, "next": {"/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303 to the app", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == tokenCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != "s3cret" {
		t.Fatalf("no token cookie set: %v", resp.Cookies())
	}
	if !cookie.HttpOnly {
		t.Error("token cookie must be HttpOnly")
	}

	// The app, with the cookie and nothing in the URL.
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.AddCookie(cookie)
	app, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	app.Body.Close()
	if app.StatusCode != http.StatusOK {
		t.Errorf("app with cookie: status %d, want 200", app.StatusCode)
	}

	// And the socket, which is what makes a token-free URL actually usable.
	ws, _ := http.NewRequest("GET", srv.URL+"/ws", nil)
	ws.AddCookie(cookie)
	sock, err := srv.Client().Do(ws)
	if err != nil {
		t.Fatal(err)
	}
	sock.Body.Close()
	if sock.StatusCode == http.StatusUnauthorized {
		t.Error("the cookie did not authenticate the WebSocket handshake — the browser would be forced back to ?token= in the URL")
	}
}

func TestBadTokenIsRefusedAndThrottled(t *testing.T) {
	srv := loginServer(t)
	post := func() *http.Response {
		resp, err := srv.Client().PostForm(srv.URL+loginPath, url.Values{"token": {"wrong"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	resp := post()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token: status %d, want 401", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == tokenCookie {
			t.Fatal("a rejected token still set the auth cookie")
		}
	}

	// A form invites guessing in a way a bare 401 does not.
	var throttled bool
	for range loginMaxFailures + 2 {
		if post().StatusCode == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Errorf("token guesses are never throttled after %d failures", loginMaxFailures)
	}
}

// next carries the denied path across the detour, but it must never carry the
// browser off this origin: "//evil.example" is a scheme-relative URL, and a
// redirect that followed it would turn the login page into an open redirect —
// an attacker-controlled bounce hosted on the operator's own trusted name.
func TestNextCannotLeaveTheOrigin(t *testing.T) {
	for _, bad := range []string{
		"//evil.example/",
		"https://evil.example/",
		"/ok\r\nX-Injected: 1",
		"",
	} {
		if got := safeNext(bad); !strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") || strings.ContainsAny(got, "\r\n") {
			t.Errorf("safeNext(%q) = %q — escapes the origin", bad, got)
		}
	}
	if got := safeNext("/sessions/abc?x=1"); got != "/sessions/abc?x=1" {
		t.Errorf("safeNext dropped a legitimate deep link: %q", got)
	}
}

// Under forward-auth there is no token to type, so offering a token box would be
// a dead end that looks like a way in.
func TestNoFormWhenThereIsNoTokenToType(t *testing.T) {
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{AuthHeader: "X-Auth-User"}))
	defer srv.Close()

	_, body := browserGet(srv, "/")
	if strings.Contains(body, `name="token"`) {
		t.Error("offered a token form on a daemon that has no token auth")
	}
	resp, err := srv.Client().PostForm(srv.URL+loginPath, url.Values{"token": {"anything"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("the login endpoint accepted a token on a daemon with no token auth")
	}
}

// browserGetCookie is a page navigation carrying a RAW Cookie header, rather
// than one built by http.Request.AddCookie, so a test can send a value AddCookie
// would sanitize away before the server ever saw it.
func browserGetCookie(srv *httptest.Server, path, cookie string) (*http.Response, string) {
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,*/*")
	if cookie != "" {
		req.Header.Set("Cookie", schemeCookie+"="+cookie)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// htmlTag returns the opening <html …> tag, which is the only place a
// data-scheme ATTRIBUTE can appear.
//
// Scoping matters more than it looks. The stylesheet contains its own
// [data-scheme='light'] / [data-scheme='dark'] SELECTORS in every response, so
// `strings.Contains(body, "data-scheme=")` is true whatever the cookie said —
// an absence assertion against the whole body can never hold, and a presence
// assertion against it passes without the attribute ever being written. The
// first draft of these tests did exactly that and failed loudly for it.
func htmlTag(body string) string {
	i := strings.Index(body, "<html")
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], ">")
	if j < 0 {
		return ""
	}
	return body[i : i+j+1]
}

// The login page cannot read localStorage: it is served under default-src 'none'
// with no script-src, and that scriptlessness is deliberate (see loginTmpl). So
// the panel mirrors the choice into a cookie and this page renders from it.
//
// Without this the login screen followed the OS while the panel it guards showed
// the opposite — most visibly in the case the control exists for, a light panel
// chosen on a dark OS.
func TestLoginPageHonoursTheSchemeCookie(t *testing.T) {
	srv := loginServer(t)
	for _, tc := range []struct{ name, cookie, want string }{
		{"chosen dark", "dark", `data-scheme="dark"`},
		{"chosen light", "light", `data-scheme="light"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body := browserGetCookie(srv, "/", tc.cookie)
			tag := htmlTag(body)
			if tag == "" {
				t.Fatalf("no <html> tag in the response; the fixture is broken: %.120q", body)
			}
			if !strings.Contains(tag, tc.want) {
				t.Errorf("cookie %q did not put %s on the root tag: %q", tc.cookie, tc.want, tag)
			}
		})
	}
}

// auto is not a third palette, it is the absence of an override, so it must
// render as NO attribute and leave the page's media query in charge. A request
// carrying no cookie at all is the same case, and is the one every first-time
// visitor makes: it must follow the OS rather than pin light.
func TestAutoAndAbsentLeaveTheLoginPageToTheOS(t *testing.T) {
	srv := loginServer(t)
	for _, tc := range []struct{ name, cookie string }{
		{"auto is not an override", "auto"},
		{"no cookie at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body := browserGetCookie(srv, "/", tc.cookie)
			tag := htmlTag(body)
			if tag == "" {
				t.Fatalf("no <html> tag in the response; the fixture is broken: %.120q", body)
			}
			if strings.Contains(tag, "data-scheme=") {
				t.Errorf("cookie %q pinned a palette; the OS should decide: %q", tc.cookie, tag)
			}
		})
	}
	// And the media arm must stay an EXCLUSION. Widening it back to :root would
	// let a dark OS override a chosen light, which is the same defect the panel's
	// stylesheet guards against in scheme.test.ts.
	_, body := browserGetCookie(srv, "/", "")
	for _, want := range []string{
		"@media (prefers-color-scheme: dark)",
		":not([data-scheme='light'])",
		":not([data-scheme='dark'])",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the inherited-dark arm lost %q; a chosen light would lose to a dark OS", want)
		}
	}
}

// The cookie is attacker-influenceable — anything able to set a cookie on this
// host chooses the value, and this page is served to the UNAUTHENTICATED. The
// allowlist in loginScheme is what keeps it out of the markup; html/template's
// escaping is the second line, not the first.
func TestAHostileSchemeCookieReachesNoMarkup(t *testing.T) {
	srv := loginServer(t)
	// Every byte here is a legal cookie-octet, so it survives Go's own parser and
	// genuinely arrives at loginScheme. A quote or a semicolon would be dropped in
	// transit and the test would prove nothing.
	hostile := "dark><script>alert(1)</script>"
	resp, body := browserGetCookie(srv, "/", hostile)

	if strings.Contains(body, "<script") {
		t.Errorf("a cookie put a script tag on the login page: %.200q", body)
	}
	if tag := htmlTag(body); strings.Contains(tag, "data-scheme=") {
		t.Errorf("an unrecognised cookie value was echoed into the root tag: %q", tag)
	}
	if strings.Contains(body, "alert(1)") {
		t.Error("the cookie value reached the page at all")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

// The guard on the whole reason this went through a cookie instead of reading
// localStorage. If a later change gives this page a script-src, the trade that
// was deliberately avoided has been made — and it would be made on the one page
// that accepts the bearer token.
func TestTheLoginPageIsStillScriptless(t *testing.T) {
	srv := loginServer(t)
	resp, body := browserGetCookie(srv, "/", "dark")

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("the login page lost its default-src 'none': %q", csp)
	}
	if strings.Contains(csp, "script-src") {
		t.Errorf("the login page gained a script-src; theming must not cost this: %q", csp)
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("the login CSP was loosened: %q", csp)
	}
	if strings.Contains(body, "<script") {
		t.Error("the login page grew a script tag")
	}
}
