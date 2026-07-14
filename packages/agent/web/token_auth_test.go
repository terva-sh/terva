//go:build terva_web

package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// postToken submits the login form, optionally as a proxy would: from loopback,
// carrying the headers haproxy/nginx add. hdr entries are applied last.
func postToken(srv *httptest.Server, token string, hdr map[string]string) *http.Response {
	form := url.Values{"token": {token}, "next": {"/"}}
	req, _ := http.NewRequest("POST", srv.URL+loginPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// A cookie with no Max-Age dies with the browser session — and an installed PWA
// is exactly the client whose session iOS ends without asking. The credential has
// to outlive the process, or a home-screen terva logs itself out every few hours.
func TestTokenCookieOutlivesTheBrowserSession(t *testing.T) {
	srv := loginServer(t)
	c := cookieNamed(postToken(srv, "s3cret", nil), tokenCookie)
	if c == nil {
		t.Fatal("a correct token set no cookie")
	}
	if c.MaxAge <= 0 {
		t.Fatalf("token cookie is a session cookie (MaxAge=%d); it will not survive a PWA relaunch", c.MaxAge)
	}
}

// The deployment that matters terminates TLS at a reverse proxy and forwards
// cleartext to a loopback listener, so r.TLS is nil on a request the user made
// over https. Deciding Secure from r.TLS alone drops the attribute on a site
// served entirely over TLS, and the browser will then offer the token to a
// plaintext request to the same host.
func TestCookieIsSecureBehindATLSTerminatingProxy(t *testing.T) {
	srv := loginServer(t)

	c := cookieNamed(postToken(srv, "s3cret", map[string]string{"X-Forwarded-Proto": "https"}), tokenCookie)
	if c == nil {
		t.Fatal("a correct token set no cookie")
	}
	if !c.Secure {
		t.Error("token cookie lacks Secure even though the browser's leg was https")
	}

	// ...and it must NOT be set on a genuinely cleartext deployment, or a
	// loopback http:// panel could never send the cookie back at all.
	if c := cookieNamed(postToken(srv, "s3cret", nil), tokenCookie); c == nil || c.Secure {
		t.Error("token cookie is Secure on a plain-http origin; the browser will withhold it")
	}
}

// GET on the login path is a browser asking for the form — the client sends the
// browser here when it finds its credential gone. It used to answer 405, which
// renders the same HTML but tells every cache and proxy in between that the
// method was the problem.
func TestGetOnTheLoginPathIsAPageNotAMethodError(t *testing.T) {
	srv := loginServer(t)
	resp, body := browserGet(srv, loginPath)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", loginPath, resp.StatusCode)
	}
	if !strings.Contains(body, `name="token"`) {
		t.Error("GET on the login path did not render the form")
	}
}

// The client bounces the browser to /auth?next=<where it was>. The form must
// carry that destination through, not its own URI — posting the login page back
// to itself lands on the form again and only reaches the app on a second hop.
func TestTheFormCarriesTheDestinationItWasSentWith(t *testing.T) {
	srv := loginServer(t)
	_, body := browserGet(srv, loginPath+"?next=%2Fsessions")
	if !strings.Contains(body, `name="next" value="/sessions"`) {
		t.Errorf("the form did not carry ?next= through; it would post itself back to the login page.\n%s", body)
	}
}

// An authenticated visitor who lands on the form wants the panel, not a demand
// for what they have already proved.
func TestAnAuthenticatedVisitorIsSentOnFromTheForm(t *testing.T) {
	srv := loginServer(t)
	req, _ := http.NewRequest("GET", srv.URL+loginPath+"?next=%2F", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: "s3cret"})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("an authenticated GET on the form = %d, want a 303 onward", resp.StatusCode)
	}
}

// The probe exists because a WebSocket handshake never reports its status: a 401
// and an unreachable daemon reach the page as the same bare close. Without a way
// to tell them apart the client can only guess, and it guessed "retry", forever.
func TestAuthStatusReportsWhatTheGateDecided(t *testing.T) {
	srv := loginServer(t)

	resp, err := srv.Client().Get(srv.URL + authStatusPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("probe without a credential = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", srv.URL+authStatusPath, nil)
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: "s3cret"})
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("probe with a good cookie = %d, want 204", resp2.StatusCode)
	}
}

// The probe is a fetch(), not a navigation, so it must come back as a status a
// script can branch on — never the login HTML, which a script cannot type into.
// These are the headers the browser puts on the client's fetch.
func TestAuthStatusAnswersAFetchWithAStatusNotAPage(t *testing.T) {
	srv := loginServer(t)
	req, _ := http.NewRequest("GET", srv.URL+authStatusPath, nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Accept", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("probe = %d, want 401", resp.StatusCode)
	}
	if strings.Contains(string(body), `name="token"`) {
		t.Error("the probe served the login form; a fetch() cannot type into it")
	}
}

// The service worker must be fetchable by a client that has NO credential, or a
// worker that has stranded itself can never be replaced by a fixed one — and the
// only cure left is deleting the home-screen app, the very thing that has to keep
// working. The app itself stays gated; only the shell is public.
func TestThePWAShellIsReachableWithoutACredential(t *testing.T) {
	srv := loginServer(t)

	for _, p := range []string{"/sw.js", "/registerSW.js", "/manifest.webmanifest"} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s without a credential = %d, want 200 — a logged-out PWA cannot heal itself", p, resp.StatusCode)
		}
	}

	// The app is not part of the shell.
	for _, p := range []string{"/index.html", "/"} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s without a credential = 200; the app must stay behind the gate", p)
		}
	}
}

// Behind a reverse proxy every request arrives FROM THE PROXY. Bucketing failed
// logins by the peer address therefore collapses every client in the world into
// one bucket, and any stranger's five bad guesses lock the operator out of their
// own panel for a minute. The real client is the rightmost X-Forwarded-For entry:
// a proxy appends the peer it saw, so a client that forges the header produces
// "<forged>, <real>" and only the tail is ours.
func TestOneClientsBadGuessesDoNotLockOutAnother(t *testing.T) {
	srv := loginServer(t)
	proxied := func(xff string) *http.Response {
		return postToken(srv, "wrong-token", map[string]string{"X-Forwarded-For": xff})
	}

	// Burn the guesser's whole budget. httptest connects from loopback, which is
	// a trusted proxy peer, so the forwarded client is the one that counts.
	for i := 0; i < loginMaxFailures+1; i++ {
		proxied("198.51.100.7")
	}
	if got := proxied("198.51.100.7").StatusCode; got != http.StatusTooManyRequests {
		t.Errorf("the guesser was not throttled: %d, want 429", got)
	}

	// The operator, behind the same proxy, is untouched.
	if got := proxied("203.0.113.4").StatusCode; got != http.StatusUnauthorized {
		t.Errorf("a different client behind the same proxy = %d, want 401 (a plain refusal, not 429 — "+
			"the throttle is bucketing everyone behind the proxy together)", got)
	}

	// A forged leading entry must not buy the guesser a clean slate: the proxy's
	// appended tail is what identifies them.
	if got := proxied("1.2.3.4, 198.51.100.7").StatusCode; got != http.StatusTooManyRequests {
		t.Errorf("a forged X-Forwarded-For prefix reset the throttle: %d, want 429", got)
	}
}

// A forwarded-for header from an UNTRUSTED peer is just a string the caller typed.
// Honoring it would hand any direct client an unlimited supply of clean slates.
func TestForwardedForFromAnUntrustedPeerIsIgnored(t *testing.T) {
	opts := Options{Token: "s3cret"}
	r, _ := http.NewRequest("POST", "/auth", nil)
	r.RemoteAddr = "198.51.100.99:5555" // not loopback, not a configured proxy
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := clientIP(opts, r); got != "198.51.100.99" {
		t.Errorf("clientIP = %q, want the peer address — an untrusted client set its own identity", got)
	}
}
