//go:build terva_web

// Package web is the WebSocket carrier + HTTP server for `terva web`: it binds
// a ctrlproto.WorkspaceService to a browser control panel. Each WebSocket
// connection is one ctrlproto peer, served by ctrlproto.ServeConn over a
// gorilla-websocket FrameConn. The panel PWA is embedded via go:embed.
//
// This whole package is gated behind the terva_web build tag (see web_stub.go
// for the no-tag placeholder), so the min binary never carries it.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// BuiltIn reports whether the web server is compiled in (true in this build).
const BuiltIn = true

// DefaultAddr is the loopback bind used when none is given.
const DefaultAddr = "127.0.0.1:8730"

// Options configures the server. It mirrors the --web-* flags.
type Options struct {
	Addr           string       // listen address; DefaultAddr if empty
	AuthHeader     string       // trusted forward-auth header (proxy asserts identity)
	TrustedProxies []*net.IPNet // peers (besides loopback) allowed to assert AuthHeader
	Token          string       // bearer token required when no forward-auth is used
	AllowInsecure  bool         // permit a non-loopback bind with no auth mode (blanket: any source)
	InsecureCIDRs  []*net.IPNet // source networks granted no-auth access (scoped insecure); permits a non-loopback bind
	Version        string       // reported in the ctrlproto hello
	Locale         string       // active UI language (BCP-47), advertised to clients
	AllowRestart   bool         // advertise the restart feature so clients show a restart control
}

// ParseTrustedProxies parses CIDR strings (e.g. "10.0.0.0/24") into networks for
// Options.TrustedProxies. A bare IP is accepted as a single-host network. Used by
// the composition root to build Options from the --web-trusted-proxy flag.
func ParseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
			continue
		}
		if ip := net.ParseIP(c); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		return nil, fmt.Errorf("invalid %q: want an IP or CIDR", c)
	}
	return out, nil
}

// Serve runs the HTTP server until ctx is cancelled or serving fails.
// It fails closed: binding a non-loopback address with no auth mode is refused
// unless AllowInsecure is set, because the endpoint can run tools as the user.
func Serve(ctx context.Context, svc ctrlproto.WorkspaceService, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if err := checkBindSafety(opts); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           newMux(ctx, svc, opts),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()

	// Bind before announcing: once Listen returns the kernel is accepting
	// connections, so the ready line is truthful — with ListenAndServe it would
	// print before the socket exists. Print the requested address, not
	// ln.Addr(): a dual-stack wildcard bind reports itself as "[::]", which
	// reads as a regression to someone who asked for 0.0.0.0.
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "terva web: ready — serving on http://%s\n", opts.Addr)
	describeAuth(opts)

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newMux builds the HTTP routes: the auth-gated WebSocket carrier, an
// unauthenticated health check, and the auth-gated embedded PWA. Extracted from
// Serve so tests can drive it via httptest without binding a port.
func newMux(ctx context.Context, svc ctrlproto.WorkspaceService, opts Options) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/ws", authMiddleware(opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWS(ctx, svc, opts, w, r)
	})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("/", authMiddleware(opts, securityHeaders(staticHandler())))
	return mux
}

// securityHeaders adds browser defense-in-depth to every static/PWA response.
// The auth gate is the primary boundary; these bound what a compromised or
// confused renderer can do afterwards. The CSP is written for the app as
// built: no inline scripts or styles (vite emits external assets; Preact's
// object-valued style props set DOM properties, which CSP does not police),
// images from self plus the data: URLs the transcript gallery builds, and
// same-origin WebSockets — 'self' alone does not match ws:// in every
// browser, so the schemes are named; exfiltration via connect-src needs
// script execution, which script-src 'self' already denies.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"connect-src 'self' ws: wss:; manifest-src 'self'; worker-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Reject cross-origin WebSocket handshakes: allow a missing Origin (native
	// clients) or one whose host matches the request host. The auth gate is the
	// primary boundary; this closes the drive-by-CSRF hole a token in a query
	// param would otherwise leave open.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
			return true
		}
		fmt.Fprintf(os.Stderr, "terva web: rejected cross-origin handshake from %s (origin %q)\n", r.RemoteAddr, origin)
		return false
	},
}

func serveWS(ctx context.Context, svc ctrlproto.WorkspaceService, opts Options, w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	c.SetReadLimit(maxFrameBytes) // bound a single frame so it can't OOM the daemon
	who := clientDesc(opts, r)
	start := time.Now()
	fmt.Fprintf(os.Stderr, "terva web: client connected — %s\n", who)
	connCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		fmt.Fprintf(os.Stderr, "terva web: client disconnected — %s (%s)\n", who, time.Since(start).Round(time.Second))
	}()
	// Unblock a parked ReadMessage when the connection ends (server shutdown or
	// this handler returning): closing the socket makes the read error out.
	go func() {
		<-connCtx.Done()
		_ = c.Close()
	}()
	conn := &wsConn{c: c}
	hello := ctrlproto.ServerHello("terva web", opts.Version)
	hello.Locale = opts.Locale
	if opts.AllowRestart {
		hello.Features = append(hello.Features, ctrlproto.FeatureRestart)
	}
	_, _ = ctrlproto.ServeConn(connCtx, conn, svc, hello)
}

// clientDesc describes a connecting client for the console log: its socket
// address, plus the forwarded-for client and the authenticated user when a
// reverse proxy fronts the daemon — so a line reads meaningfully behind
// Authentik, not just as the proxy's address.
func clientDesc(opts Options, r *http.Request) string {
	desc := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		desc += " (forwarded-for " + strings.TrimSpace(xff) + ")"
	}
	if opts.AuthHeader != "" {
		if u := r.Header.Get(opts.AuthHeader); u != "" {
			desc += " user=" + u
		}
	}
	return desc
}

// checkBindSafety refuses fail-open configurations on a non-loopback bind. A
// bearer token protects it directly. A forward-auth header protects it ONLY when
// the proxy is identifiable — loopback is moot for a public bind, so a
// --web-trusted-proxy CIDR is required; the header is otherwise forgeable by any
// client that can reach the port (and authorized() would ignore it, leaving the
// endpoint effectively unreachable-auth), so we fail closed at startup with a
// clear message rather than silently.
func checkBindSafety(opts Options) error {
	// A scoped insecure listener (--web-insecure-cidr) is a deliberate no-auth
	// bind whose boundary is the source-IP allowlist, so it may bind non-loopback
	// just like the blanket --web-insecure — authorized() enforces the scope.
	if isLoopbackAddr(opts.Addr) || opts.AllowInsecure || len(opts.InsecureCIDRs) > 0 {
		return nil
	}
	if opts.Token != "" {
		return nil
	}
	if opts.AuthHeader != "" && len(opts.TrustedProxies) > 0 {
		return nil
	}
	if opts.AuthHeader != "" {
		return fmt.Errorf("terva web: refusing to bind non-loopback address %q under forward-auth with no --web-trusted-proxy: header %q is forgeable from the network — name the proxy's CIDR with --web-trusted-proxy, or run the proxy on the same host and bind loopback, or pass --web-insecure to override", opts.Addr, opts.AuthHeader)
	}
	return fmt.Errorf("terva web: refusing to bind non-loopback address %q with no auth: set --web-token or --web-auth-header (front it with a reverse proxy), or pass --web-insecure to override", opts.Addr)
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "", "0.0.0.0", "::":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
