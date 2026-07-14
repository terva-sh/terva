//go:build terva_web

package web

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The login page exists to keep the bearer token out of the URL.
//
// Before it, a browser had exactly one way in: ?token=<secret> on the address
// bar, which the middleware then exchanged for a cookie. That single visit is
// enough to spill the token into the fronting proxy's access log, the browser's
// history and autocomplete, and the Referer of anything the page later links to
// — none of which terva controls, and all of which outlive the session. A form
// POST puts the secret in a request body instead, where none of those record it.
//
// ?token= still works: it is how you hand someone a one-click link, and removing
// it would break the PWA install flow. This just means nobody has to use it.

// loginPath is where the form posts, and where a client whose credential has
// gone stale sends the browser. It sits outside authMiddleware for the obvious
// reason. authStatusPath is the credential probe (see newMux); it sits INSIDE the
// gate, because its whole job is to report what the gate decided.
const (
	loginPath      = "/auth"
	authStatusPath = "/auth/status"
)

// loginTmpl is deliberately one self-contained page with no script: a login form
// that depended on the bundled PWA could not be served to a client that is not
// allowed to fetch the bundle yet. The style is inlined under a per-response
// nonce so the strict style-src survives — see loginCSP.
var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>terva</title>
<style nonce="{{.Nonce}}">
  :root { color-scheme: light dark; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 15px/1.5 ui-sans-serif, system-ui, sans-serif;
         background: #fbfbfa; color: #1c1b1a; }
  main { width: min(22rem, calc(100vw - 3rem)); }
  h1 { font-size: 1.1rem; font-weight: 600; margin: 0 0 .25rem; }
  p  { margin: 0 0 1.25rem; color: #6b6a67; font-size: .875rem; }
  label { display: block; font-size: .8125rem; font-weight: 500; margin-bottom: .375rem; }
  input, button { width: 100%; box-sizing: border-box; border-radius: .375rem;
                  font: inherit; padding: .5rem .625rem; }
  input { border: 1px solid #d6d3d1; background: #fff; color: inherit; }
  input:focus { outline: 2px solid #b45309; outline-offset: -1px; border-color: transparent; }
  button { margin-top: .75rem; border: 0; background: #1c1b1a; color: #fbfbfa;
           font-weight: 500; cursor: pointer; }
  button:hover { background: #3a3836; }
  .err { color: #b91c1c; font-size: .8125rem; margin: 0 0 .75rem; }
  @media (prefers-color-scheme: dark) {
    body { background: #1c1b1a; color: #e7e5e4; }
    p { color: #a8a29e; }
    input { background: #292725; border-color: #44403c; }
    button { background: #e7e5e4; color: #1c1b1a; }
    button:hover { background: #fff; }
  }
</style>
</head>
<body>
<main>
  <h1>terva</h1>
  <p>This control panel needs its bearer token.</p>
  {{if .Err}}<p class="err">{{.Err}}</p>{{end}}
  <form method="post" action="{{.Action}}">
    <label for="token">Token</label>
    <input id="token" name="token" type="password" autocomplete="current-password"
           autofocus required spellcheck="false">
    <input type="hidden" name="next" value="{{.Next}}">
    <button type="submit">Connect</button>
  </form>
</main>
</body>
</html>
`))

// loginCSP is the app CSP minus what a scriptless page cannot need, plus the
// nonce that lets the one <style> block run. form-action is 'self' rather than
// the app's 'none' — the whole page is a form, and it posts to us.
func loginCSP(nonce string) string {
	return "default-src 'none'; style-src 'nonce-" + nonce + "'; form-action 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; object-src 'none'"
}

// serveLogin renders the form. status is 401 on a gate failure and 200 when the
// operator asked for the page.
func serveLogin(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	n := base64.RawStdEncoding.EncodeToString(nonce)

	h := w.Header()
	h.Set("Content-Security-Policy", loginCSP(n))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	// next carries the path the browser was denied, so a deep link survives the
	// detour. Only a local path — an absolute URL here would make the login page
	// an open redirect, handing an attacker a terva-hosted bounce to anywhere.
	//
	// When the denied request WAS the login page (the client bounces the browser
	// here on a 401, carrying the real destination in ?next=), its own URI is not
	// the destination — posting it back would land on the form again and only
	// reach the app on a second hop.
	next := r.URL.RequestURI()
	if r.URL.Path == loginPath {
		next = r.URL.Query().Get("next")
	}
	_ = loginTmpl.Execute(w, struct{ Nonce, Err, Action, Next string }{
		Nonce:  n,
		Err:    errMsg,
		Action: loginPath,
		Next:   safeNext(next),
	})
}

// safeNext keeps a post-login redirect target on this origin: one leading slash,
// never two (`//evil.example` is a scheme-relative URL that browsers follow
// off-site), and no control characters to smuggle a header past the redirect.
func safeNext(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	if strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}

// handleLogin takes the posted token. On a match it sets the same cookie the
// ?token= path sets and sends the browser on; the WebSocket handshake carries
// cookies, so nothing downstream needs the token in a URL ever again.
func handleLogin(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// No token configured means no token to type; the form should never
		// have been offered, and accepting a POST here would be theatre.
		if opts.Token == "" {
			http.Error(w, "no token auth is configured", http.StatusNotFound)
			return
		}
		// GET is a browser ASKING for the form — the client bounces here when it
		// finds its credential gone, and an operator may simply navigate here. It
		// is a page, so it answers 200. (It used to answer 405, which renders the
		// same HTML but tells every cache and proxy between us that the method was
		// the problem.) An already-authenticated visitor wants the panel, not a
		// form demanding what they have already proved.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if authorized(opts, r) {
				http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
				return
			}
			serveLogin(w, r, http.StatusOK, "")
			return
		}
		if r.Method != http.MethodPost {
			serveLogin(w, r, http.StatusMethodNotAllowed, "")
			return
		}
		who := clientIP(opts, r)
		if !loginAttemptAllowed(who) {
			fmt.Fprintf(os.Stderr, "terva web: throttling token guesses from %s\n", who)
			serveLogin(w, r, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
			return
		}
		if err := r.ParseForm(); err != nil {
			serveLogin(w, r, http.StatusBadRequest, "Malformed submission.")
			return
		}
		if !constantTimeEqual(strings.TrimSpace(r.PostFormValue("token")), opts.Token) {
			loginFailed(who)
			fmt.Fprintf(os.Stderr, "terva web: rejected bad token from %s\n", who)
			serveLogin(w, r, http.StatusUnauthorized, "That token was not accepted.")
			return
		}
		loginSucceeded(who)
		setTokenCookie(w, r, opts.Token)
		http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
	}
}

// A form invites guessing in a way a bare 401 does not, so failures are
// throttled per source address: a short burst, then a cooling-off minute. The
// token is compared in constant time either way, and a token generated the way
// the docs say (openssl rand -hex 24) is far out of reach of any online guess —
// this bounds the damage when someone picks a memorable one instead.
const (
	loginMaxFailures = 5
	loginWindow      = time.Minute
)

var loginFailures sync.Map // remote IP -> *loginRecord

type loginRecord struct {
	mu    sync.Mutex
	n     int
	until time.Time
}

// The bucket key is a client IP (see clientIP), never an IP:port — every request
// from one client arrives on a fresh ephemeral port, so keying on the pair would
// hand a guesser an unlimited supply of clean slates — and never the bare peer
// address, which behind a reverse proxy is the proxy and so is the SAME for every
// caller: one stranger's five bad guesses would lock out the operator too.
func loginAttemptAllowed(ip string) bool {
	v, ok := loginFailures.Load(ip)
	if !ok {
		return true
	}
	rec := v.(*loginRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if time.Now().After(rec.until) {
		rec.n = 0 // window elapsed; forgive
		return true
	}
	return rec.n < loginMaxFailures
}

func loginFailed(ip string) {
	v, _ := loginFailures.LoadOrStore(ip, &loginRecord{})
	rec := v.(*loginRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if time.Now().After(rec.until) {
		rec.n = 0
	}
	rec.n++
	rec.until = time.Now().Add(loginWindow)
}

func loginSucceeded(ip string) { loginFailures.Delete(ip) }

// wantsLoginPage reports whether this request is a browser asking for a page, as
// opposed to curl, the PWA's own asset fetches, or a native ctrlproto client —
// which all want the plain 401 and its WWW-Authenticate header, not HTML.
func wantsLoginPage(opts Options, r *http.Request) bool {
	if opts.Token == "" {
		return false // nothing to type
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// Sec-Fetch-Mode: navigate is the browser saying "this is a page load",
	// which is exactly the question. Fall back to Accept for the browsers and
	// proxies that strip it.
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
