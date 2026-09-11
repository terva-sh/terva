package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/ctrlproto/ctrlclient"
)

// MemberOptions configures a [Member].
type MemberOptions struct {
	// HubURL is the hub's base URL, ws:// or wss://. The endpoint path and
	// the origin are appended by [MemberURL].
	HubURL string

	// Token is the shared bearer this member presents. Required.
	Token string

	// Origin is this member's name in the fleet, and it becomes the prefix of
	// every federated id the hub mints for this member's sessions. It must
	// satisfy ctrlproto.ValidOrigin.
	Origin string

	// Service is the workspace this member serves to the hub. Required. It is
	// the same ctrlproto.WorkspaceService the daemon serves to a browser, so a
	// member grants the hub exactly what a local client already has.
	Service ctrlproto.WorkspaceService

	// Hello is this member's SERVER hello. Note the asymmetry against the
	// transport: this process dials, and it still answers as the server,
	// because ctrlproto roles follow who serves the workspace rather than who
	// opened the socket.
	Hello ctrlproto.Hello

	// Backoff is the pause between dial attempts. Zero takes DefaultBackoff.
	Backoff time.Duration

	// OnServed fires when a served connection ends, with the reason. It does
	// not fire for a failed dial, matching ctrlclient.OnDisconnect.
	OnServed func(err error)
}

// Member is one terva daemon checking in to a hub.
//
// It dials out and then serves, which is the whole trick. The socket opens in
// the direction reachability actually runs, and the protocol runs the other
// way over it.
type Member struct {
	opts MemberOptions
	dial func(ctx context.Context) (ctrlproto.FrameConn, error)
}

// NewMember builds a member. It does not dial; call [Member.Run].
func NewMember(opts MemberOptions) (*Member, error) {
	if opts.Service == nil {
		return nil, errors.New("fleet: a member needs a Service to serve")
	}
	if opts.Token == "" {
		return nil, errors.New("fleet: a member needs a token to present to the hub")
	}
	if !ctrlproto.ValidOrigin(opts.Origin) {
		return nil, fmt.Errorf("fleet: %q is not a usable origin: it must be non-empty and free of %q, because it prefixes every federated id the hub mints for this member", opts.Origin, ctrlproto.FederatedIDSep)
	}
	target, err := MemberURL(opts.HubURL, opts.Origin)
	if err != nil {
		return nil, err
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	return &Member{opts: opts, dial: ctrlclient.DialWebSocket(target, opts.Token)}, nil
}

// Run checks in and serves until ctx ends, redialling with backoff.
//
// The loop mirrors ctrlclient.Run on purpose, because it is the same loop from
// the other end: dial, serve one connection, pause, dial again. The member is
// the end that CAN redial, so unlike the hub's parked dial this one really
// does retry, and a hub that is down or restarting is an ordinary wait rather
// than a failure.
func (m *Member) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := m.dial(ctx)
		if err == nil {
			err = m.serve(ctx, conn)
			if m.opts.OnServed != nil && ctx.Err() == nil {
				m.opts.OnServed(err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.opts.Backoff):
		}
	}
}

// serve runs the server side of ctrlproto over one dialled connection.
//
// ServeConn reads the hello first and the hub's ctrlclient writes it first, so
// this end waits for the peer that did not open the socket to speak. That is
// correct, and it is worth knowing because getting it backwards produces a
// hang rather than an error: both ends would sit in ReadFrame.
func (m *Member) serve(ctx context.Context, conn ctrlproto.FrameConn) error {
	defer func() { _ = conn.Close() }()
	// Cancellation reaches a blocked ReadFrame only by closing the transport,
	// the same posture ctrlclient.serve takes.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	_, err := ctrlproto.ServeConn(ctx, conn, m.opts.Service, m.opts.Hello)
	return err
}
