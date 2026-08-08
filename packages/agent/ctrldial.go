package agent

// Dialing a running terva over ctrlproto, for the commands that are CLIENTS of a
// daemon rather than an agent of their own.
//
// `terva ext config` grew this first and `terva ctl` needed the same forty
// lines: normalize the endpoint, pick the websocket or unix dialer, start the
// client's run loop, and poll Connected() until a deadline — carrying the last
// dial error across a goroutine boundary so the timeout can say WHY rather than
// just that it expired. Written twice it would have drifted twice; the failure
// text is the part that matters and the part nobody re-derives correctly.
//
// Deliberately not a general "connect to terva" API. It is the shared middle of
// two CLI commands, and it stops where they differ: what to ask for, and what to
// do with the answer.

import (
	"context"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/ctrlproto/ctrlclient"
	"terva.sh/terva/packages/i18n"
)

// ctrlConn is a connected client plus the stop that tears down both its run
// goroutine and the socket. The caller ALWAYS owns the stop, including on the
// paths where it then fails — a half-open client outlives a returned error.
type ctrlConn struct {
	Client *ctrlclient.Client
	Stop   func()
	Addr   string
}

// dialCtrl connects to a serving terva and waits for the handshake.
//
// agent names the caller in the hello (it shows in the daemon's log, so "which
// of my tools did that" is answerable). A dial that never connects reports the
// last transport error when there was one, because "no handshake within 5s" and
// "connection refused" send you to completely different places.
func dialCtrl(ctx context.Context, endpoint, token, agent, version string, timeout time.Duration) (*ctrlConn, error) {
	ep, err := normalizeAttachURL(endpoint)
	if err != nil {
		return nil, err
	}
	dial := ctrlclient.DialWebSocket(ep.URL, token)
	if ep.UnixPath != "" {
		dial = ctrlclient.DialWebSocketUnix(ep.UnixPath, token)
	}
	// The dial runs on the client's goroutine and the wait below runs on this
	// one, so the last dial error crosses a goroutine boundary and needs the
	// lock — it is read while a reconnect may be writing it.
	var (
		dialMu  sync.Mutex
		dialErr error
	)
	client, err := ctrlclient.New(ctrlclient.Options{
		Dial: func(ctx context.Context) (ctrlproto.FrameConn, error) {
			conn, derr := dial(ctx)
			if derr != nil {
				dialMu.Lock()
				dialErr = derr
				dialMu.Unlock()
			}
			return conn, derr
		},
		Hello: ctrlproto.Hello{Agent: agent, Version: version},
	})
	if err != nil {
		return nil, err
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	go func() { _ = client.Run(runCtx) }()
	stop := func() {
		cancelRun()
		_ = client.Close()
	}

	deadline := time.Now().Add(timeout)
	for !client.Connected() {
		if time.Now().After(deadline) {
			stop()
			dialMu.Lock()
			derr := dialErr
			dialMu.Unlock()
			if derr != nil {
				return nil, i18n.Errorf("cannot reach a terva at %s: %v (is it running there? does it need a token?)", ep, derr)
			}
			return nil, i18n.Errorf("no ctrlproto handshake from %s within %s", ep, timeout)
		}
		select {
		case <-ctx.Done():
			stop()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return &ctrlConn{Client: client, Stop: stop, Addr: ep.String()}, nil
}
