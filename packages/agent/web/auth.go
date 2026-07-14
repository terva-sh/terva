//go:build terva_web

package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/config"
)

// authMiddleware gates every request. The auth model is deliberately thin —
// terva's identity is single-user, so this is a gate ("keep strangers out"),
// not an identity system. Real OIDC belongs in a reverse proxy (Authentik).
func authMiddleware(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(opts, r) {
			fmt.Fprintf(os.Stderr, "terva web: rejected request with disallowed Host %q from %s\n", r.Host, r.RemoteAddr)
			explainHostRejection(r)
			http.Error(w, "forbidden: no auth is configured, so only a loopback Host is accepted (DNS-rebinding defense) — see the terva web log", http.StatusForbidden)
			return
		}
		if !authorized(opts, r) {
			// Surface failed auth: on an exposed endpoint this is the signal an
			// operator most wants (probes, a wrong/expired token). Health checks
			// are unauthenticated, so they never reach here to spam the log.
			// clientDesc, not RemoteAddr: behind a proxy the latter is the proxy,
			// so every line would name 127.0.0.1 and say nothing about who called.
			fmt.Fprintf(os.Stderr, "terva web: rejected unauthorized %s %s from %s\n", r.Method, r.URL.Path, clientDesc(opts, r))
			// A browser navigating here gets the form instead of the word
			// "unauthorized", so the token has somewhere to go that isn't the
			// address bar. Everything else — curl, the PWA's asset fetches, a
			// native ctrlproto client — still gets the plain 401 it can act on.
			if wantsLoginPage(opts, r) {
				serveLogin(w, r, http.StatusUnauthorized, "")
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// A browser that presented the token in the URL gets it back as a cookie,
		// so the fingerprinted asset subresources it then fetches (no header, no
		// query) and a later PWA launch authenticate without the token in every
		// URL. Only when the token itself was presented — never for forward-auth.
		if opts.Token != "" && constantTimeEqual(requestToken(r), opts.Token) {
			setTokenCookie(w, r, opts.Token)
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed defends the no-auth loopback listener against DNS rebinding: a
// malicious page can rebind its own hostname to 127.0.0.1 and, because the
// browser then opens a same-origin WebSocket, sail through the Origin==Host
// check — but the Host header carries the ATTACKER's name, not a loopback name.
// In no-auth mode (a loopback bind) we therefore require a loopback Host.
// When an auth mode is set, the token/proxy gate is the boundary and legitimate
// proxy hostnames vary, so the Host is not restricted.
//
// The explicit insecure modes opt out of the loopback-Host assumption for the
// peers they trust: the blanket --web-insecure trusts any peer, and
// --web-insecure-cidr trusts a source inside the allowlist (so a client reaching
// the listener by its real IP/name over a trusted overlay works). A loopback
// SOURCE is deliberately NOT auto-trusted here — only if a CIDR names it — so a
// scoped listener keeps the DNS-rebinding defense for the local browser.
func hostAllowed(opts Options, r *http.Request) bool {
	// A unix-socket listener is unreachable to a browser (no fetch/WebSocket
	// API dials a filesystem socket), so DNS rebinding cannot occur and the
	// Host header is whatever placeholder the native client used. The socket
	// file's permissions are the boundary.
	if opts.unixListener {
		return true
	}
	if opts.Token != "" || opts.AuthHeader != "" {
		return true
	}
	if len(opts.InsecureCIDRs) > 0 {
		if remoteInNetworks(r.RemoteAddr, opts.InsecureCIDRs) {
			return true
		}
	} else if opts.AllowInsecure {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]") // unwrap an IPv6 literal
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// hostRejectionHelp fires at most once per process.
var hostRejectionHelp sync.Once

// explainHostRejection says why the request was refused and what to do about it.
//
// The bare rejection line reads like a missing allowlist, and the first instinct
// it provokes is to hunt for a --web-allow-host flag. No such flag exists, and
// none should: the Host check is not an allowlist to widen but the no-auth
// mode's DNS-rebinding defense, and configuring ANY auth removes it (hostAllowed
// returns early). An operator who takes the allowlist reading spends the whole
// investigation looking for the wrong knob — so spell out the real shape here,
// once, at the moment of the failure.
//
// When a proxy is fronting the daemon, this rejection is more than an
// inconvenience: it means a reverse proxy is publishing an endpoint that has no
// authentication behind it, and everything the agent can do is on offer to
// whoever can reach the proxy. Say so plainly. The forwarding headers are the
// giveaway — forgeable, but this is advice, not a boundary.
func explainHostRejection(r *http.Request) {
	proxied := r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Forwarded-Proto") != "" ||
		r.Header.Get("X-Forwarded-Host") != "" ||
		r.Header.Get("Forwarded") != ""
	hostRejectionHelp.Do(func() {
		p := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "terva web:   "+format+"\n", a...) }
		if proxied {
			p("^ that request came through a reverse proxy, and this daemon has NO auth.")
			p("  The Host check is all that is standing between the proxy's callers and an unauthenticated agent.")
		} else {
			p("^ this daemon has NO auth, so it accepts only a loopback Host (a DNS-rebinding defense).")
		}
		p("  The check is not an allowlist to extend — it exists ONLY while there is no auth, and any auth mode lifts it:")
		p("    TERVA_WEB_TOKEN=<secret>   bearer auth, out of the command line (or --web-token-file PATH)")
		p("    --web-auth-header <header>  trust a forward-auth header from the proxy (with --web-trusted-proxy)")
		p("    --web-insecure             keep no auth at all — only if the network really is your boundary")
		p("  Full deployment guide: %s", filepath.Join(config.TervaHome(), "docs", "web.md"))
	})
}

// authorized applies, in order: no-auth (only reachable on a loopback bind,
// enforced at startup); a matching bearer token; or the trusted forward-auth
// header — but ONLY when the request came from the fronting proxy.
func authorized(opts Options, r *http.Request) bool {
	if opts.Token == "" && opts.AuthHeader == "" {
		// No-auth mode. A unix-socket listener's boundary is the socket
		// file's permissions — source-IP scoping is meaningless there. A
		// scoped insecure TCP listener (--web-insecure-cidr) admits loopback
		// + the named source networks only; a blanket --web-insecure admits
		// any source; otherwise startup guaranteed a loopback bind.
		if opts.unixListener {
			return true
		}
		if len(opts.InsecureCIDRs) > 0 {
			return remoteFromTrustedProxy(r.RemoteAddr, opts.InsecureCIDRs)
		}
		return true
	}
	if opts.Token != "" && constantTimeEqual(requestToken(r), opts.Token) {
		return true
	}
	// A forward-auth header is a PROXY assertion — trustworthy only if the request
	// actually traversed the proxy. The header is trivially forgeable by anyone
	// who can reach the port directly, so honor it ONLY from a loopback peer
	// (same-host proxy) or a configured --web-trusted-proxy CIDR. Without this,
	// binding a non-loopback address under header auth would let any client on the
	// network send the header and gain full owner access.
	if opts.AuthHeader != "" && r.Header.Get(opts.AuthHeader) != "" && remoteFromTrustedProxy(r.RemoteAddr, opts.TrustedProxies) {
		return true
	}
	return false
}

// remoteFromTrustedProxy reports whether remoteAddr (host:port) is a peer we
// trust to assert the forward-auth header, or to reach a scoped insecure
// listener: a loopback address (same-host) or an IP inside one of nets.
func remoteFromTrustedProxy(remoteAddr string, nets []*net.IPNet) bool {
	ip := remoteIP(remoteAddr)
	return ip != nil && (ip.IsLoopback() || ipInNetworks(ip, nets))
}

// remoteInNetworks reports whether remoteAddr is inside one of nets. Unlike
// remoteFromTrustedProxy it does NOT auto-trust loopback, so a scoped insecure
// listener keeps the loopback-Host DNS-rebinding check for the local browser.
func remoteInNetworks(remoteAddr string, nets []*net.IPNet) bool {
	ip := remoteIP(remoteAddr)
	return ip != nil && ipInNetworks(ip, nets)
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(host)
}

func ipInNetworks(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// tokenCookie carries the bearer token once the browser has presented it (in a
// header or the URL), so fingerprinted asset subresources — which the browser
// fetches with neither — and a later PWA launch authenticate without the token
// riding every URL. It is HttpOnly + SameSite=Strict; the WebSocket Origin check
// stays the cross-site boundary.
const tokenCookie = "terva_token"

// requestToken extracts the bearer token from the Authorization header, the
// session cookie, or — because the browser WebSocket API cannot set request
// headers — a `token` query parameter on the handshake URL, in that order. NOTE:
// a token in a URL can leak into a fronting proxy's access logs, browser history,
// and the Referer header; terva's own logs never record the query string (auth
// failures log URL.Path only), and the cookie bootstrap keeps the token out of
// subresource URLs, but for a hardened deployment prefer bearer auth from a
// native client, or front with a proxy that terminates auth (--web-auth-header)
// so no token rides the URL at all. See docs/web.md.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(rest)
		}
	}
	if c, err := r.Cookie(tokenCookie); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// tokenCookieMaxAge outlives the browser session on purpose. A cookie with no
// Max-Age is a SESSION cookie, and an installed PWA is exactly the client that
// gets its session torn down without warning — iOS evicts the web app's process
// whenever it wants memory. A session cookie therefore meant a home-screen terva
// silently lost its credential every few hours and could only be revived by
// re-typing the token, which is precisely the friction the login form exists to
// remove. The token itself does not expire (it is a file the operator writes), so
// the cookie's lifetime is a convenience horizon, not a security one; rotating
// the token invalidates every outstanding cookie on the next request.
const tokenCookieMaxAge = 30 * 24 * 60 * 60 // seconds

// setTokenCookie persists a validated token as a cookie so subsequent same-origin
// requests (assets, the WebSocket handshake, a PWA relaunch) authenticate without
// it in the URL. No-op when the request already carried the matching cookie.
func setTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	if c, err := r.Cookie(tokenCookie); err == nil && c.Value == token {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   tokenCookieMaxAge,
	})
}

// requestIsHTTPS reports whether the BROWSER's leg of this request was TLS, which
// is not the same question as whether ours was: the common deployment terminates
// TLS at a reverse proxy and forwards cleartext to a loopback listener, so r.TLS
// is nil on a request the user made over https. Trusting only r.TLS there drops
// the Secure attribute from the token cookie on a site served entirely over TLS,
// and the browser will then hand that cookie to a plaintext request to the same
// host — a redirect to https happens too late, the secret is already on the wire.
//
// X-Forwarded-Proto is forgeable, but forging it is not useful: the worst it does
// is ADD Secure to a cookie that did not need it, which withholds the cookie from
// cleartext requests. That fails closed, so honoring the header unconditionally is
// safe in a way that honoring a forwarded IP or a forward-auth header is not.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP is the source address terva is willing to make a DECISION on — today,
// which bucket a failed login counts against. Behind a reverse proxy every request
// arrives from the proxy (127.0.0.1 for a same-host haproxy/nginx), so keying on
// RemoteAddr collapses every client in the world into one bucket and lets any
// stranger's bad guesses lock the operator out. The real client is in
// X-Forwarded-For — but only the part our own proxy appended.
//
// So: take the RIGHTMOST entry, and only from a peer trusted to have written it.
// A proxy appends the peer it saw to whatever the client sent, so a client that
// forges "X-Forwarded-For: 1.2.3.4" produces "1.2.3.4, <real>" — the leftmost
// entry is the attacker's to choose, the rightmost is our proxy's. (clientDesc
// takes the leftmost instead; it is descriptive text for a human reading the log,
// never a decision, and the leftmost is the more useful one to print when several
// proxies are chained.)
func clientIP(opts Options, r *http.Request) string {
	if remoteFromTrustedProxy(r.RemoteAddr, opts.TrustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
				if ip := net.ParseIP(last); ip != nil {
					return ip.String()
				}
			}
		}
	}
	if ip := remoteIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// describeAuth prints the active auth stance to stderr at startup so the
// operator can see, at a glance, what is protecting the endpoint.
func describeAuth(opts Options) {
	switch {
	case opts.AuthHeader != "":
		where := "from loopback (same-host proxy)"
		if len(opts.TrustedProxies) > 0 {
			where = fmt.Sprintf("from loopback or %d trusted-proxy network(s)", len(opts.TrustedProxies))
		}
		fmt.Fprintf(os.Stderr, "terva web: trusting forward-auth header %q %s — front this with your reverse proxy\n", opts.AuthHeader, where)
	case opts.Token != "":
		fmt.Fprintln(os.Stderr, "terva web: bearer-token auth enabled")
	case len(opts.InsecureCIDRs) > 0:
		fmt.Fprintf(os.Stderr, "terva web: NO per-request auth — access limited to loopback + %d insecure CIDR(s); the network is your boundary\n", len(opts.InsecureCIDRs))
	case opts.AllowInsecure:
		fmt.Fprintln(os.Stderr, "terva web: NO auth on an insecure bind (--web-insecure) — anyone who can reach the port has full access")
	default:
		fmt.Fprintln(os.Stderr, "terva web: no auth (loopback only) — front with a proxy or set --web-token before exposing")
	}
}
