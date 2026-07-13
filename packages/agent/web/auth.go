//go:build terva_web

package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// authMiddleware gates every request. The auth model is deliberately thin —
// terva's identity is single-user, so this is a gate ("keep strangers out"),
// not an identity system. Real OIDC belongs in a reverse proxy (Authentik).
func authMiddleware(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(opts, r) {
			fmt.Fprintf(os.Stderr, "terva web: rejected request with disallowed Host %q from %s\n", r.Host, r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !authorized(opts, r) {
			// Surface failed auth: on an exposed endpoint this is the signal an
			// operator most wants (probes, a wrong/expired token). Health checks
			// are unauthenticated, so they never reach here to spam the log.
			fmt.Fprintf(os.Stderr, "terva web: rejected unauthorized %s %s from %s\n", r.Method, r.URL.Path, r.RemoteAddr)
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
		Secure:   r.TLS != nil,
	})
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
