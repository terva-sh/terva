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
