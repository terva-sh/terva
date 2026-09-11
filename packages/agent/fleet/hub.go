package fleet

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/ctrlproto/ctrlclient"
)

// MemberPath is the endpoint a member dials to check in.
const MemberPath = "/fleet/member"

// OriginParam names the query parameter carrying the member's origin.
const OriginParam = "origin"

// ErrHubClosed is returned once the hub has been closed.
var ErrHubClosed = errors.New("fleet: hub closed")

// HubOptions configures a [Hub].
type HubOptions struct {
	// Token is the shared bearer a member presents. Required. It names the
	// fleet rather than the machine, which is why CheckMemberBindSafety keeps
	// this endpoint off the network until fleet-identity replaces it.
	Token string

	// Hello is the hub's side of the handshake. ctrlclient forces Role to
	// "client", which is correct here: the hub drives, the member serves.
	Hello ctrlproto.Hello

	// Backoff is the pause between a member client's reconnect passes. Zero
	// takes ctrlclient.DefaultBackoff.
	Backoff time.Duration

	// OnMemberUp fires after a member completes the handshake, with the
	// member's server hello. OnMemberDown fires when an established member
	// connection drops.
	OnMemberUp   func(origin string, server ctrlproto.Hello)
	OnMemberDown func(origin string, err error)
}

// Hub accepts member check-ins and drives each member through ctrlclient.
//
// One member is one [ctrlclient.Client], which is what makes this a hub rather
// than a proxy: every shipped consumer of a WorkspaceService works against a
// member unchanged, through ctrlclient.Service.
type Hub struct {
	opts HubOptions

	mu      sync.Mutex
	members map[string]*member
	closed  bool
}

// NewHub builds a hub. It does not listen; pass a listener to [Hub.Serve], or
// mount [Hub.Handler] on a server of your own.
func NewHub(opts HubOptions) (*Hub, error) {
	if opts.Token == "" {
		return nil, errors.New("fleet: a hub needs a token: a member endpoint with no bearer would accept any caller that can reach it")
	}
	return &Hub{opts: opts, members: map[string]*member{}}, nil
}

// member is one checked-in daemon and the client driving it.
type member struct {
	origin string
	client *ctrlclient.Client
	cancel context.CancelFunc

	// incoming carries the next accepted connection to a blocked dial.
	//
	// Buffered by one, for a reason worth stating. ctrlclient waits Backoff
	// AFTER a connection drops and BEFORE it dials again, so a member that
	// checks straight back in arrives while nothing is reading. Unbuffered,
	// that check-in would block the HTTP handler; buffered, it waits in hand
	// and the next dial collects it immediately.
	incoming chan ctrlproto.FrameConn

	// dials counts calls to dial, so a test can show the loop parks instead
	// of spinning against an absent member.
	dials atomic.Int64
}

// dial hands ctrlclient the connection the member opened.
//
// This is the reconnect decision the ticket asked to be made and written down.
//
// ctrlclient.Run loops dial, serve, wait Backoff, dial, for the life of its
// context. That shape assumes the client can reopen the transport. A hub
// cannot: the member owns the socket and the hub has no route back to it,
// which is the entire premise of check-in. A dial that returned an error would
// spin at the backoff cadence forever against a member that can never be
// dialled, burning a goroutine and a log line per pass, per absent member.
//
// So dial blocks. It parks on the next check-in, which turns the shipped
// reconnect loop into exactly the right behaviour without changing a line of
// it: the loop stops at the dial until the member comes back, then resumes
// through the ordinary handshake and resubscribe path.
//
// The alternative was lifting reconnect out of ctrlclient entirely. That means
// a second implementation of handshake, pending-call rejection, and
// subscription teardown, free to drift from the one ctrlclient already has.
// Blocking here costs one parked goroutine per member instead.
func (m *member) dial(ctx context.Context) (ctrlproto.FrameConn, error) {
	m.dials.Add(1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c, ok := <-m.incoming:
		if !ok {
			return nil, ErrHubClosed
		}
		return c, nil
	}
}

// offer parks a freshly accepted connection for the next dial.
//
// A connection already waiting is closed and dropped. A member that checks in
// twice before the hub's dial comes round has left a stale socket behind, and
// the newer one is the one whose reachability was just proven.
func (m *member) offer(c ctrlproto.FrameConn) {
	select {
	case stale := <-m.incoming:
		_ = stale.Close()
	default:
	}
	select {
	case m.incoming <- c:
	default:
		_ = c.Close()
	}
}

// Handler serves the member endpoint. ctx governs every member client the hub
// starts, so cancelling it stops the fleet.
func (h *Hub) Handler(ctx context.Context) http.Handler {
	up := &websocket.Upgrader{
		// A member is a daemon, not a page. There is no browser origin to
		// police here, and a dialling daemon sends no Origin header at all,
		// so the default check would refuse every member. The bearer below
		// is the gate.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	mux := http.NewServeMux()
	mux.HandleFunc(MemberPath, func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		origin := r.URL.Query().Get(OriginParam)
		if !ctrlproto.ValidOrigin(origin) {
			http.Error(w, "invalid origin", http.StatusBadRequest)
			return
		}
		// Upgrade before registering the member. A caller that clears both
		// gates but is not speaking websocket would otherwise start a client
		// and a parked goroutine for a member that never arrives.
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade has already written its own response.
			return
		}
		c.SetReadLimit(ctrlproto.MaxFrameBytes)
		m, err := h.memberFor(ctx, origin)
		if err != nil {
			_ = c.Close()
			return
		}
		// Hand off and return. gorilla has hijacked the socket, so the
		// connection outlives this handler; the member client owns it now.
		m.offer(&wsConn{c: c})
	})
	return mux
}

// authorized compares the presented bearer in constant time, so a caller
// cannot walk the token a byte at a time off the response latency.
func (h *Hub) authorized(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.opts.Token)) == 1
}

// memberFor returns the member for origin, starting its client on first sight.
func (h *Hub) memberFor(ctx context.Context, origin string) (*member, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	if m, ok := h.members[origin]; ok {
		return m, nil
	}
	m := &member{origin: origin, incoming: make(chan ctrlproto.FrameConn, 1)}
	client, err := ctrlclient.New(ctrlclient.Options{
		Dial:    m.dial,
		Hello:   h.opts.Hello,
		Backoff: h.opts.Backoff,
		OnConnect: func(server ctrlproto.Hello) {
			if h.opts.OnMemberUp != nil {
				h.opts.OnMemberUp(origin, server)
			}
		},
		OnDisconnect: func(err error) {
			if h.opts.OnMemberDown != nil {
				h.opts.OnMemberDown(origin, err)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.client = client
	h.members[origin] = m
	go func() { _ = client.Run(runCtx) }()
	return m, nil
}

// Client returns the client driving one member. Wrap it with
// ctrlclient.Service to get a ctrlproto.WorkspaceService for that member.
func (h *Hub) Client(origin string) (*ctrlclient.Client, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.members[origin]
	if !ok {
		return nil, false
	}
	return m.client, true
}

// Members lists the origins that have checked in at least once, in a stable
// order. A member that has gone away stays listed: it is expected back, and
// its client is parked in dial waiting for it.
func (h *Hub) Members() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.members))
	for origin := range h.members {
		out = append(out, origin)
	}
	sort.Strings(out)
	return out
}

// dialCount reports how many times a member's dial has been entered. It backs
// the test that shows an absent member parks the loop rather than spinning.
func (h *Hub) dialCount(origin string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.members[origin]
	if !ok {
		return 0
	}
	return m.dials.Load()
}

// Close stops every member client and releases their parked dials.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	members := make([]*member, 0, len(h.members))
	for _, m := range h.members {
		members = append(members, m)
	}
	h.mu.Unlock()

	for _, m := range members {
		m.cancel()
		_ = m.client.Close()
	}
	return nil
}

// Serve runs the member endpoint on ln until ctx ends.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: h.Handler(ctx)}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = srv.Close()
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// MemberURL builds the URL a member dials for this hub endpoint.
func MemberURL(base, origin string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + MemberPath
	q := u.Query()
	q.Set(OriginParam, origin)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
