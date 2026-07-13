// Package ctrlclient is the client side of ctrlproto — the Go twin of the web
// PWA's client.ts, as a reusable library. It dials a serving peer (terva web
// today; a unix-socket daemon later), performs the hello handshake, correlates
// command/response frames, fans events out to per-session subscriptions, and
// reconnects with backoff when the connection drops.
//
// It is deliberately connection-count-agnostic and rendering-free: `terva
// attach` holds one of these, a fleet/orchestration frontend holds N, and a
// test harness drives a live daemon with a three-line program (see
// docs/proposals/archive/tui-attach.md).
//
// Reliability contract: commands on a down connection fail fast with
// [ErrNotConnected]; in-flight commands are rejected with [ErrDisconnected]
// when the connection drops; and subscription channels CLOSE on disconnect (or
// overflow). A closed subscription channel is the resync signal — the consumer
// re-subscribes (the server's first event on any subscribe is a full snapshot),
// exactly the discipline the web PWA follows.
package ctrlclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

var (
	// ErrNotConnected is returned by Call when no connection is up. Callers
	// treat it like the web client's "not connected" rejection: fail the
	// operation cleanly and rely on the reconnect + resubscribe + snapshot
	// cycle to resync.
	ErrNotConnected = errors.New("ctrlclient: not connected")
	// ErrDisconnected rejects in-flight calls when the connection drops
	// before their response arrives.
	ErrDisconnected = errors.New("ctrlclient: connection lost")
	// ErrClosed is returned once Close has been called.
	ErrClosed = errors.New("ctrlclient: closed")
)

// DefaultBackoff is the delay between reconnect attempts — the same flat
// cadence the web PWA uses.
const DefaultBackoff = 1500 * time.Millisecond

// subChanBuffer bounds a subscription channel. A consumer that falls this far
// behind is force-closed (the resync signal) rather than stalling the read
// loop — the snapshot it re-subscribes into is cheaper than an unbounded
// buffer during a streaming turn.
const subChanBuffer = 256

// Options configures a [Client].
type Options struct {
	// Dial opens one transport connection. Required. Called for the initial
	// connect and every reconnect; ctx is the Run context.
	Dial func(ctx context.Context) (ctrlproto.FrameConn, error)

	// Hello is this client's side of the handshake. Role is forced to
	// "client"; Protocol defaults to [ctrlproto.Protocol] when zero. Leaving
	// Groups nil advertises the conversation, session, and control groups.
	Hello ctrlproto.Hello

	// OnConnect fires after each successful handshake with the server's
	// hello (version, features, locale) — the version-skew surface.
	OnConnect func(server ctrlproto.Hello)
	// OnDisconnect fires when an established connection drops (not on
	// failed dial attempts), with the read-loop error.
	OnDisconnect func(err error)
	// OnEvent, when set, observes every event frame before subscription
	// fan-out — a raw tap for loggers/recorders. Subscriptions are the
	// normal consumption path.
	OnEvent func(sess string, ev ctrlproto.Event)

	// Backoff is the delay between reconnect attempts (default
	// [DefaultBackoff]). Reconnection continues until Run's ctx ends or
	// Close is called.
	Backoff time.Duration
}

// Client is a reconnecting ctrlproto client. Construct with [New], start with
// [Run] (usually on its own goroutine), issue commands with [Call] or through
// the [ctrlproto.WorkspaceService] view returned by [Service].
type Client struct {
	opts Options

	mu     sync.Mutex
	conn   ctrlproto.FrameConn // nil when down
	server ctrlproto.Hello     // last server hello (valid when connected once)
	seen   bool                // server hello received at least once
	up     bool
	closed bool
	nextID uint64
	// pending correlates in-flight command ids to their response slot.
	// Reset (rejected) on every disconnect.
	pending map[uint64]chan ctrlproto.Frame
	// subs fans event frames out per session. A subscriber that overflows
	// or outlives its connection is closed — the resync signal.
	subs map[string][]*subscription

	wmu sync.Mutex // serializes WriteFrame across command writers
}

type subscription struct {
	sess string
	ch   chan ctrlproto.Event
	once sync.Once
}

func (s *subscription) close() { s.once.Do(func() { close(s.ch) }) }

// New builds a client. Run must be started for anything to connect.
func New(opts Options) (*Client, error) {
	if opts.Dial == nil {
		return nil, errors.New("ctrlclient: Options.Dial is required")
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	opts.Hello.Role = ctrlproto.RoleClient
	if opts.Hello.Protocol == 0 {
		opts.Hello.Protocol = ctrlproto.Protocol
	}
	if opts.Hello.Groups == nil {
		opts.Hello.Groups = []ctrlproto.Group{
			ctrlproto.GroupConversation, ctrlproto.GroupSession, ctrlproto.GroupControl,
		}
	}
	return &Client{
		opts:    opts,
		pending: map[uint64]chan ctrlproto.Frame{},
		subs:    map[string][]*subscription{},
	}, nil
}

// Run dials and serves the connection until ctx ends or [Close] is called,
// reconnecting with backoff whenever the connection drops. It returns ctx's
// error (or nil after Close). Run must be called at most once.
func (c *Client) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.isClosed() {
			return nil
		}
		conn, err := c.opts.Dial(ctx)
		if err == nil {
			err = c.serve(ctx, conn)
			if c.opts.OnDisconnect != nil && ctx.Err() == nil && !c.isClosed() {
				c.opts.OnDisconnect(err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.opts.Backoff):
		}
	}
}

// serve performs the handshake and runs the read loop on one connection,
// returning when it drops. It owns conn's lifecycle.
func (c *Client) serve(ctx context.Context, conn ctrlproto.FrameConn) error {
	defer conn.Close()
	// Close the transport when ctx ends so a blocking ReadFrame unblocks
	// (the ws carrier reads without honoring ctx, by design — see web/conn.go).
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := conn.WriteFrame(ctx, ctrlproto.HelloFrame(c.opts.Hello)); err != nil {
		return err
	}
	first, err := conn.ReadFrame(ctx)
	if err != nil {
		return err
	}
	if first.Kind != ctrlproto.KindHello || first.Hello == nil {
		return fmt.Errorf("ctrlclient: expected server hello, got %q", first.Kind)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.conn = conn
	c.server = *first.Hello
	c.seen = true
	c.up = true
	c.mu.Unlock()

	if c.opts.OnConnect != nil {
		c.opts.OnConnect(*first.Hello)
	}

	readErr := c.readLoop(ctx, conn)
	c.teardownConn()
	return readErr
}

func (c *Client) readLoop(ctx context.Context, conn ctrlproto.FrameConn) error {
	for {
		f, err := conn.ReadFrame(ctx)
		if err != nil {
			return err
		}
		switch f.Kind {
		case ctrlproto.KindResp:
			c.mu.Lock()
			slot, ok := c.pending[f.ID]
			if ok {
				delete(c.pending, f.ID)
			}
			c.mu.Unlock()
			if ok {
				slot <- f // buffered(1); never blocks
			}
		case ctrlproto.KindEvent:
			if f.Event == nil {
				continue
			}
			if c.opts.OnEvent != nil {
				c.opts.OnEvent(f.Sess, *f.Event)
			}
			c.dispatchEvent(f.Sess, *f.Event)
		default:
			// Server-sent cmd/hello frames are not expected; ignore rather
			// than tearing down, mirroring ServeConn's posture.
		}
	}
}

// dispatchEvent fans one event out to sess's subscribers. A full subscriber
// buffer means the consumer stopped draining: close it (the resync signal)
// instead of blocking the read loop or buffering without bound.
func (c *Client) dispatchEvent(sess string, ev ctrlproto.Event) {
	c.mu.Lock()
	subs := append([]*subscription(nil), c.subs[sess]...)
	c.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			c.removeSub(s)
			s.close()
		}
	}
}

// teardownConn marks the connection down, rejects every in-flight call, and
// closes every subscription channel (their resync signal).
func (c *Client) teardownConn() {
	c.mu.Lock()
	c.conn = nil
	c.up = false
	pending := c.pending
	c.pending = map[uint64]chan ctrlproto.Frame{}
	var all []*subscription
	for _, list := range c.subs {
		all = append(all, list...)
	}
	c.subs = map[string][]*subscription{}
	c.mu.Unlock()

	for _, slot := range pending {
		slot <- ctrlproto.Frame{Kind: ctrlproto.KindResp, Error: &ctrlproto.Error{
			Code: ctrlproto.CodeInternal, Message: ErrDisconnected.Error(),
		}}
	}
	for _, s := range all {
		s.close()
	}
}

// Close stops the client: the current connection is torn down and Run returns
// after its next loop check. Idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Connected reports whether a handshaken connection is currently up.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.up
}

// ServerHello returns the most recent server hello (version, features,
// locale) and whether any handshake has completed yet. The hello survives a
// disconnect so a frontend can keep naming the build it was attached to.
func (c *Client) ServerHello() (ctrlproto.Hello, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.server, c.seen
}

// Call issues one command and decodes its result into result (which may be
// nil for bare-ok responses). A server-reported failure comes back as a
// *[ctrlproto.Error] with its wire code preserved. Fails fast with
// [ErrNotConnected] when the connection is down.
func (c *Client) Call(ctx context.Context, sess string, method ctrlproto.Method, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	conn := c.conn
	if conn == nil || !c.up {
		c.mu.Unlock()
		return ErrNotConnected
	}
	c.nextID++
	id := c.nextID
	slot := make(chan ctrlproto.Frame, 1)
	c.pending[id] = slot
	c.mu.Unlock()

	f, err := ctrlproto.CmdFrame(id, sess, method, params)
	if err != nil {
		c.dropPending(id)
		return err
	}
	c.wmu.Lock()
	err = conn.WriteFrame(ctx, f)
	c.wmu.Unlock()
	if err != nil {
		c.dropPending(id)
		return err
	}

	select {
	case <-ctx.Done():
		c.dropPending(id)
		return ctx.Err()
	case resp := <-slot:
		if resp.Error != nil {
			if resp.Error.Message == ErrDisconnected.Error() {
				return ErrDisconnected
			}
			return resp.Error
		}
		if result == nil {
			return nil
		}
		return resp.BindResult(result)
	}
}

func (c *Client) dropPending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Subscribe opens an event stream for sess: it registers a local fan-out
// channel, then issues the wire subscribe (idempotent server-side, so N local
// subscribers cost one server pump). The returned channel closes when ctx
// ends, the connection drops, or the consumer falls too far behind — in every
// case re-subscribing yields a fresh snapshot first, which is the resync
// contract. Cancelling the LAST local subscriber for sess sends the wire
// unsubscribe (best-effort).
func (c *Client) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	sub := &subscription{sess: sess, ch: make(chan ctrlproto.Event, subChanBuffer)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if !c.up {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	c.subs[sess] = append(c.subs[sess], sub)
	c.mu.Unlock()

	if err := c.Call(ctx, sess, ctrlproto.MethodSubscribe, nil, nil); err != nil {
		c.removeSub(sub)
		sub.close()
		return nil, err
	}

	// Unsubscribe on ctx end. AfterFunc (not a goroutine-per-subscribe
	// leak): fires once, detached by the sub's own close path being
	// idempotent.
	context.AfterFunc(ctx, func() {
		last := c.removeSub(sub)
		sub.close()
		if last && c.Connected() {
			// Best-effort; the server also reaps subscriptions on disconnect.
			_ = c.Call(context.Background(), sess, ctrlproto.MethodUnsubscribe, nil, nil)
		}
	})
	return sub.ch, nil
}

// removeSub unlinks sub from the registry and reports whether it was the last
// local subscriber for its session.
func (c *Client) removeSub(sub *subscription) (last bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.subs[sub.sess]
	for i, s := range list {
		if s == sub {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(c.subs, sub.sess)
		return true
	}
	c.subs[sub.sess] = list
	return false
}
