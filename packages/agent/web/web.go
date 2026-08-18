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
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// BuiltIn reports whether the web server is compiled in (true in this build).
const BuiltIn = true

// DefaultAddr is the loopback bind used when none is given.
const DefaultAddr = "127.0.0.1:8730"

// Options configures the server. It mirrors the --web-* flags.
type Options struct {
	Addr           string       // listen address: host:port, or unix:/path for a filesystem socket; DefaultAddr if empty
	AuthHeader     string       // trusted forward-auth header (proxy asserts identity)
	TrustedProxies []*net.IPNet // peers (besides loopback) allowed to assert AuthHeader
	Token          string       // bearer token required when no forward-auth is used
	AllowInsecure  bool         // permit a non-loopback bind with no auth mode (blanket: any source)
	InsecureCIDRs  []*net.IPNet // source networks granted no-auth access (scoped insecure); permits a non-loopback bind
	Version        string       // reported in the ctrlproto hello
	Locale         string       // active UI language (BCP-47), advertised to clients
	CWD            string       // workspace working directory, advertised in the hello
	Jailed         bool         // workspace sandbox lock state, advertised in the hello
	AllowRestart   bool         // advertise the restart feature so clients show a restart control
	// AllowLogin advertises the AUTH GROUP: this daemon will add, repair, and
	// revoke MODEL-PROVIDER credentials. Off the base ServerHello, like the replay
	// group — a client that negotiates it is guaranteed the verbs work, and one
	// that does not see it renders no login controls at all. Never enabled on an
	// unauthenticated listener (see web_mode.go).
	AllowLogin bool

	// AllowSecrets advertises the SECRETS GROUP: this daemon will report its
	// at-rest encryption posture and manage the secret store's grants. Off the
	// base ServerHello for the same reason AllowLogin is, and separate from it
	// because the two grant different authority — see [ctrlproto.GroupSecrets].
	// Never enabled on an unauthenticated listener (see web_mode.go).
	AllowSecrets bool

	// AllowStage mounts the Stage app at /stage/ and advertises the `stage`
	// feature in the hello, so the panel offers an "open in Stage" link. Off by
	// default (--web-stage) — Stage is the immersive chat/play surface, a distinct
	// product from the control panel, opted into per deployment.
	AllowStage bool

	// OnListen is called once, after the control listener binds and before any
	// connection is served, with the listener's network ("unix" or "tcp") and
	// its address. It reports what was ACTUALLY bound, which is the only honest
	// source under systemd socket activation — the unit chooses the address and
	// Addr never held it.
	//
	// The composition root uses this to publish where the daemon can be reached
	// (see config.PublishListenRecord). It lives here as a callback rather than
	// a write from this package because web owns a transport, not state under
	// $TERVA_HOME.
	OnListen func(network, addr string)

	// unixListener marks the effective listener as a unix domain socket —
	// set by Serve (from the unix: address form or an adopted systemd
	// socket), never by callers. The socket file's permissions are the auth
	// boundary there: the loopback-Host and no-auth posture checks key on
	// this, and IP-based options (TrustedProxies, InsecureCIDRs) are
	// meaningless for it. A Token, if set, is still enforced on top.
	unixListener bool
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
//
// Three listener sources, in precedence order: a systemd socket-activation fd
// (LISTEN_FDS, either family — the .socket unit decides), a `unix:/path`
// address (filesystem socket, 0600 — the permissions are the auth boundary),
// or a TCP host:port. Binding happens before announcing: once Listen returns
// the kernel is accepting connections, so the ready line is truthful.
func Serve(ctx context.Context, svc ctrlproto.WorkspaceService, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}

	ln, display, err := listenControl(opts)
	if err != nil {
		return err
	}
	opts.unixListener = ln.Addr().Network() == "unix"
	// A systemd-activated TCP socket binds whatever the unit says, not
	// opts.Addr — apply the fail-closed posture to the address it actually
	// bound. (The unix: and plain-TCP paths were checked in listenControl,
	// before their sockets existed.)
	if !opts.unixListener {
		effective := opts
		effective.Addr = ln.Addr().String()
		if err := checkBindSafety(effective); err != nil {
			_ = ln.Close()
			return err
		}
	}

	srv := &http.Server{
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

	if opts.OnListen != nil {
		opts.OnListen(ln.Addr().Network(), ln.Addr().String())
	}

	fmt.Fprintf(os.Stderr, "terva web: ready — serving on %s\n", display)
	if opts.unixListener {
		fmt.Fprintln(os.Stderr, "terva web: auth = the socket file's permissions (plus the bearer token, if one is set)")
	} else {
		describeAuth(opts)
	}

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// listenControl produces the control listener plus its display string. The
// display prints the REQUESTED address for a plain TCP bind, not ln.Addr():
// a dual-stack wildcard bind reports itself as "[::]", which reads as a
// regression to someone who asked for 0.0.0.0.
func listenControl(opts Options) (net.Listener, string, error) {
	if ln, err := systemdListener(); err != nil {
		return nil, "", err
	} else if ln != nil {
		net_ := ln.Addr().Network()
		if net_ == "unix" {
			return ln, "unix:" + ln.Addr().String() + " (systemd socket)", nil
		}
		return ln, "http://" + ln.Addr().String() + " (systemd socket)", nil
	}
	if path := unixSocketPath(opts.Addr); path != "" {
		ln, err := listenUnix(path)
		if err != nil {
			return nil, "", err
		}
		return ln, "unix:" + path, nil
	}
	if err := checkBindSafety(opts); err != nil {
		return nil, "", err
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, "", err
	}
	return ln, "http://" + opts.Addr, nil
}

// unixSocketPath extracts the filesystem path from a unix:-form listen
// address — "unix:/run/terva.sock" or "unix:///run/terva.sock" — and returns
// "" for anything else (a TCP host:port).
func unixSocketPath(addr string) string {
	rest, ok := strings.CutPrefix(addr, "unix:")
	if !ok {
		return ""
	}
	return strings.TrimPrefix(rest, "//")
}

// listenUnix binds a filesystem socket with 0600 permissions — the file IS
// the auth boundary. A stale socket left by a crashed daemon is cleared
// first; a live daemon answers the probe dial and the bind is refused
// instead of stealing its socket. Anything at the path that is NOT a socket is
// refused outright and left on disk — never removed.
func listenUnix(path string) (net.Listener, error) {
	// Lstat, not Stat: the mode has to be the mode of the path itself, and a
	// symlink is not a socket we put there. A failed dial cannot stand in for
	// "stale socket" — every regular file fails it too — and the removal is
	// irreversible, so the socket bit has to be checked rather than assumed.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("terva web: %s already exists and is not a socket (%s) — refusing to remove it; pick another path or delete it yourself", path, fi.Mode().Type())
		}
		if c, derr := net.DialTimeout("unix", path, 500*time.Millisecond); derr == nil {
			_ = c.Close()
			return nil, fmt.Errorf("terva web: %s is already served by a running daemon", path)
		}
		if rerr := os.Remove(path); rerr != nil {
			return nil, fmt.Errorf("terva web: stale socket %s: %w", path, rerr)
		}
		fmt.Fprintf(os.Stderr, "terva web: removed stale socket %s\n", path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Tighten to owner-only right after the bind (Listen creates it under
	// the umask). The window is a few instructions wide and the daemon has
	// accepted nothing yet.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("terva web: chmod %s: %w", path, err)
	}
	return ln, nil
}

// sdSocket pins the systemd-inherited fd 3 open for the process lifetime.
// net.FileListener dups the fd, and letting the *os.File be collected would
// close fd 3 itself — which must survive: it carries no CLOEXEC (systemd
// cleared it) and syscall.Exec keeps the pid, so a Tier-1 self-restart's new
// image re-reads LISTEN_PID/LISTEN_FDS, finds them valid, and re-adopts this
// very fd. That is what makes /restart work under socket activation.
var sdSocket *os.File

// systemdListener adopts a socket-activation fd (sd_listen_fds(3) without
// the dependency): LISTEN_PID must name this process and LISTEN_FDS must be
// exactly 1 — terva serves one control endpoint. Returns (nil, nil) when not
// socket-activated. The LISTEN_* env is deliberately NOT scrubbed (see
// sdSocket: the restart re-exec needs it).
func systemdListener() (net.Listener, error) {
	fdsEnv := os.Getenv("LISTEN_FDS")
	if fdsEnv == "" {
		return nil, nil
	}
	if pid := os.Getenv("LISTEN_PID"); pid != strconv.Itoa(os.Getpid()) {
		return nil, nil // activation aimed at some other process; ignore
	}
	n, err := strconv.Atoi(fdsEnv)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("terva web: malformed LISTEN_FDS=%q", fdsEnv)
	}
	if n > 1 {
		return nil, fmt.Errorf("terva web: %d systemd sockets passed; terva serves exactly one — declare one ListenStream in the .socket unit", n)
	}
	f := os.NewFile(3, "systemd-socket")
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("terva web: adopt systemd socket: %w", err)
	}
	sdSocket = f
	return ln, nil
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
	// The login form posts here, so it sits OUTSIDE authMiddleware — a gate that
	// turned away the request carrying the token would be a closed loop. The
	// handler does its own constant-time check and throttles guesses.
	mux.HandleFunc(loginPath, handleLogin(opts))
	// The credential probe. It exists because the WebSocket API is silent about
	// WHY a handshake failed: a 401 and an unreachable daemon both surface to the
	// page as a bare close event with no status, so the panel cannot tell "your
	// token expired" from "the machine is asleep" — and, unable to tell, it did
	// the only safe thing and reconnected forever, in silence. This answers the
	// question over plain HTTP, where the status code survives: 204 authenticated,
	// 401 not. It is a GET with no body, so the login form is never served here —
	// wantsLoginPage only fires for a navigation.
	mux.Handle(authStatusPath, authMiddleware(opts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})))
	// The PWA shell — manifest, icons, service worker — is served unauthenticated
	// so an installed app that has lost its cookie can still boot far enough to
	// show the login form and to pull a newer service worker. See pwaShellPaths.
	for _, p := range pwaShellPaths() {
		mux.Handle(p, securityHeaders(shellHandler()))
	}
	// ...and so is the fingerprinted bundle, because the service worker PRECACHES it
	// and a precache entry it cannot fetch is a worker that cannot install at all.
	// This is the rule, not an exception to it: nothing in the precache manifest may
	// sit behind the gate. TestThePrecacheManifestIsFetchableWithoutACredential holds
	// the line. index.html is NOT under this prefix and stays gated, which is what
	// makes an unauthenticated navigation answer with the login form.
	mux.Handle(assetsPrefix, securityHeaders(shellHandler()))
	// Library media (card avatars, later backgrounds) — auth-gated like /ws, and
	// deliberately NOT in the precache manifest: it serves user content from
	// $TERVA_HOME, which must never boot without a credential. Kept above the "/"
	// catch-all only for readability; ServeMux matches the longest prefix.
	mux.Handle("/media/", authMiddleware(opts, securityHeaders(http.HandlerFunc(serveMedia))))
	// File attachments the user drops on the composer. Auth-gated like /media/,
	// and same-origin checked on top: a multipart POST is a CORS-simple request,
	// so without that check a hostile page could write into the staging area of a
	// no-auth loopback daemon. Never precached, for the same reason /media/ isn't.
	// The bound is passed, not read inside the route, so this line is the single
	// place it is decided — the same value serveWS advertises as
	// MaxAttachmentBytes, which is what a client sizes a file against before
	// spending a long upload on it.
	mux.Handle(uploadPath, authMiddleware(opts, securityHeaders(uploadHandler(attach.NewStore(), attach.MaxBytes))))
	// Files the AGENT shared back. Auth-gated like the two above and never
	// precached; it sets its own Content-Security-Policy over the app's, because
	// unlike /media/ the bytes here were chosen by the model. No origin check:
	// this is a GET a browser makes by navigating or by an <img>/<audio> src, so
	// there is often no Origin at all, and reading a file the owner's own agent
	// produced is not a state change to defend against.
	//
	// The store is passed for uploadHandler's reason: which area is served is
	// this line's decision, not something the route reaches out and decides.
	mux.Handle(sharedPath, authMiddleware(opts, securityHeaders(sharedHandler(attach.NewShareStore()))))
	// The Stage app (second MPA entry), mounted only when web_stage is enabled.
	// ServeMux matches the longer /stage/ prefix over "/".
	if opts.AllowStage {
		// Stage's bundle, service worker, manifests, and icons are served OUTSIDE
		// the gate under /stage/, mirroring the panel's root exception: the
		// /stage/-scoped worker PRECACHES them, and a precache entry the client
		// cannot fetch is a worker that cannot install (see stagePwaShellPaths and
		// TestStagePrecacheFetchableWithoutACredential). It is the same
		// content-addressed shared bundle already ungated at /assets/, and holds no
		// token or session. These specific routes are matched ahead of the gated
		// /stage/ catch-all by ServeMux's longest-prefix rule.
		for _, p := range stagePwaShellPaths() {
			mux.Handle(p, securityHeaders(stageShellHandler()))
		}
		mux.Handle("/stage/assets/", securityHeaders(stageShellHandler()))
		// The Stage shell (stage.html) and its client routes stay gated, so an
		// unauthenticated navigation to /stage/ answers with the login form.
		mux.Handle("/stage/", authMiddleware(opts, securityHeaders(stageHandler())))
	}
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
	CheckOrigin: func(r *http.Request) bool { return sameOrigin(r, "handshake") },
}

// sameOrigin reports whether a request may be honored given its Origin header.
//
// A missing Origin is allowed: native clients (terva attach, a curl script) send
// none, and the header is not something a browser lets a page forge. An Origin
// whose host matches the request's Host is same-origin and allowed. Anything
// else is a cross-site request and refused.
//
// Both state-changing browser-reachable entry points share this. The WebSocket
// needs it because a token in a query param would otherwise be drive-by
// usable; the upload route needs it because multipart/form-data is a CORS-SIMPLE
// content type, so the browser sends the POST without a preflight and only
// withholds the response — by which time the file is already staged. In no-auth
// loopback mode (hostAllowed accepts a loopback Host) that is the only thing
// standing between a hostile page and the staging area.
//
// what names the rejected operation in the log line.
func sameOrigin(r *http.Request, what string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	fmt.Fprintf(os.Stderr, "terva web: rejected cross-origin %s from %s (origin %q)\n", what, r.RemoteAddr, origin)
	return false
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
	// Keep the socket alive across a silent turn (a proxy reads silence as death)
	// and reap it if the peer stops answering. Both halves live in conn.go.
	conn.armReadDeadline()
	go conn.keepalive(connCtx)
	hello := buildHello(opts, maxUploadBytes, attach.MaxBytes)
	if _, err := ctrlproto.ServeConn(connCtx, conn, svc, hello); err != nil {
		logConnEnd(who, err)
	}
}

// buildHello maps this daemon's Options onto the ctrlproto hello — the
// features, groups and limits the whole browser client keys off.
//
// A function rather than eighteen lines inside serveWS, because inside serveWS
// it needed a live WebSocket to observe and so was never tested at all: the only
// assertion made on the server hello anywhere was that its protocol is 1. A
// wrong mapping here does not fail; it silently removes a control from every
// client, which reads as "the feature does not exist".
func buildHello(opts Options, maxUploadBytes, maxAttachmentBytes int64) ctrlproto.Hello {
	hello := ctrlproto.ServerHello("terva web", opts.Version)
	hello.Locale = opts.Locale
	hello.CWD = opts.CWD
	hello.Jailed = opts.Jailed
	hello.MaxUploadBytes = maxUploadBytes
	// This carrier mounts POST /upload, so a client may stage files and name
	// them on a prompt. Advertised here rather than in the base hello because it
	// is the CARRIER that can take bytes — a native client on a unix socket has
	// no such route, and should not offer a drop target that goes nowhere.
	hello.Features = append(hello.Features, ctrlproto.FeatureAttachments)
	hello.MaxAttachmentBytes = maxAttachmentBytes
	// ...and GET /shared/, so the panel can turn a share record into something
	// the user can actually click. Same carrier-not-protocol reasoning.
	hello.Features = append(hello.Features, ctrlproto.FeatureSharedFiles)
	if opts.AllowRestart {
		hello.Features = append(hello.Features, ctrlproto.FeatureRestart)
	}
	if opts.AllowLogin {
		hello.Groups = append(hello.Groups, ctrlproto.GroupAuth)
	}
	if opts.AllowSecrets {
		hello.Groups = append(hello.Groups, ctrlproto.GroupSecrets)
	}
	if opts.AllowStage {
		hello.Features = append(hello.Features, ctrlproto.FeatureStage)
	}
	return hello
}

// logConnEnd explains why a connection ended, for the ends that are NOT a client
// simply going away. This error used to be discarded, and one case in particular
// was undiagnosable because of it: a frame over maxFrameBytes makes gorilla close
// the socket from under an in-flight request, so the browser reports a generic
// dead-socket error ("not connected") while the daemon logged only its usual
// "client disconnected". Nothing on either side named the size, which is the one
// fact that identifies the problem.
func logConnEnd(who string, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
		return // our own shutdown closing the socket, not a fault
	case websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway):
		return // the tab closed or navigated away
	}
	if errors.Is(err, websocket.ErrReadLimit) || websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		fmt.Fprintf(os.Stderr,
			"terva web: %s sent a frame larger than the %d-byte limit — connection closed. "+
				"A file upload inflates ~4/3 in base64, so the practical file ceiling is ~%d bytes.\n",
			who, maxFrameBytes, maxUploadBytes)
		return
	}
	fmt.Fprintf(os.Stderr, "terva web: connection ended — %s: %v\n", who, err)
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
