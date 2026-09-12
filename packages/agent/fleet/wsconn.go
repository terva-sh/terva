package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// wsConn adapts an accepted gorilla WebSocket to a ctrlproto.FrameConn. It is
// the accept-side twin of ctrlclient's dial-side wrapper, and it frames
// identically: ctrlproto.Encode and Decode, one frame per text message.
//
// Both ends call the same codec rather than each reaching for encoding/json on
// its own. Two hand-rolled copies of a frame codec agree right up until one of
// them is changed.
type wsConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
	// pongWait is how long this socket may go without traffic before the read
	// deadline reaps it. armReadDeadline fills in the package default when it
	// is zero, so a test can inject a short one and not sit out 65 seconds.
	pongWait time.Duration
}

var _ ctrlproto.FrameConn = (*wsConn)(nil)

// The hub is the end that pings, and both ends carry a read deadline. That is
// not symmetry for its own sake, because the two deadlines catch different
// failures and neither one covers the other.
//
// The hub's deadline catches a member that died without closing: a host that
// lost power, a process killed with no chance to unwind. Nothing is left on the
// far end to notice anything, so only the hub can free the source.
//
// The member's deadline catches the tunnel dying underneath two live processes.
// The hub reaps its end and moves on, but the member never sees that close,
// because the path carrying it is the thing that broke. ctrlclient.Run only
// re-dials when a connection ERRORS, so without a deadline of its own the
// member sits forever believing it is checked in. The hub reaping alone would
// shrink the fleet rather than heal it.
//
// Only the hub pings, and that is deliberate. Traffic from either end resets a
// middlebox idle timer, so one pinger is enough to keep the socket open. The
// member then proves the path is alive by the pings arriving, which is why its
// deadline is pushed by a received ping (see ctrlclient's DialWebSocketReaping).
// A second pinger would buy nothing and double the idle chatter of a fleet that
// is mostly idle by design.

// keepalive pings until ctx ends, so an idle member socket keeps traffic on the
// wire and a member that stopped answering is reaped rather than leaked. Runs
// for the life of one connection; the caller starts it in a goroutine.
//
// The ping shares WriteFrame's mutex because gorilla forbids concurrent
// writers, control frames included, and a ping racing a streamed frame corrupts
// the stream.
func (w *wsConn) keepalive(ctx context.Context) { w.keepaliveEvery(ctx, pingInterval) }

// keepaliveEvery is keepalive with the interval injected, so a test can prove
// an idle socket is pinged without sitting out a production interval to do it.
func (w *wsConn) keepaliveEvery(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.wmu.Lock()
			_ = w.c.SetWriteDeadline(time.Now().Add(writeTimeout))
			err := w.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
			w.wmu.Unlock()
			if err != nil {
				return // the socket is gone; the read side will notice and tear down
			}
		}
	}
}

// armReadDeadline starts the dead-member clock and arranges for every pong to
// push it back out. Reads refresh it too (see ReadFrame): a member that is
// talking is alive whether or not it happened to pong.
func (w *wsConn) armReadDeadline() {
	if w.pongWait <= 0 {
		w.pongWait = pongWait
	}
	_ = w.c.SetReadDeadline(time.Now().Add(w.pongWait))
	w.c.SetPongHandler(func(string) error {
		return w.c.SetReadDeadline(time.Now().Add(w.pongWait))
	})
}

// ReadFrame blocks on the next message. Cancellation arrives by way of the
// owner closing the socket, which makes ReadMessage return an error. That is
// the same posture both shipped wrappers take, and it is why ServeConn and
// ctrlclient both install a context.AfterFunc that closes the transport.
func (w *wsConn) ReadFrame(_ context.Context) (ctrlproto.Frame, error) {
	_, data, err := w.c.ReadMessage()
	if err != nil {
		return ctrlproto.Frame{}, err
	}
	// Traffic is liveness. Without this a member that talks steadily but never
	// pongs would be reaped mid-session. Guarded on pongWait because a wsConn
	// whose deadline was never armed must not get one of now plus zero.
	if w.pongWait > 0 {
		_ = w.c.SetReadDeadline(time.Now().Add(w.pongWait))
	}
	return ctrlproto.Decode(data)
}

func (w *wsConn) WriteFrame(_ context.Context, f ctrlproto.Frame) error {
	b, err := ctrlproto.Encode(f)
	if err != nil {
		return err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_ = w.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	return w.c.WriteMessage(websocket.TextMessage, b)
}

func (w *wsConn) Close() error { return w.c.Close() }
