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
		next.ServeHTTP(w, r)
	})
}

// hostAllowed defends the no-auth loopback listener against DNS rebinding: a
// malicious page can rebind its own hostname to 127.0.0.1 and, because the
// browser then opens a same-origin WebSocket, sail through the Origin==Host
// check — but the Host header carries the ATTACKER's name, not a loopback name.
// In no-auth mode (always a loopback bind) we therefore require a loopback Host.
// When an auth mode is set, the token/proxy gate is the boundary and legitimate
// proxy hostnames vary, so the Host is not restricted.
func hostAllowed(opts Options, r *http.Request) bool {
	if opts.Token != "" || opts.AuthHeader != "" {
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
		return true // no auth mode; startup guaranteed a loopback bind
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
// trust to assert the forward-auth header: a loopback address (same-host proxy)
// or an IP inside one of the configured trusted-proxy networks.
func remoteFromTrustedProxy(remoteAddr string, proxies []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range proxies {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// requestToken extracts the bearer token from the Authorization header, or —
// because the browser WebSocket API cannot set request headers — from a `token`
// query parameter on the handshake URL. NOTE: a token in a URL can leak into a
// fronting proxy's access logs, browser history, and the Referer header; terva's
// own logs never record the query string (auth failures log URL.Path only), but
// for a hardened deployment prefer bearer auth from a native client, or front
// with a proxy that terminates auth (--web-auth-header) so no token rides the
// URL. See docs/web.md.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return r.URL.Query().Get("token")
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
	default:
		fmt.Fprintln(os.Stderr, "terva web: no auth (loopback only) — front with a proxy or set --web-token before exposing")
	}
}
