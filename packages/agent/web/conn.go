//go:build terva_web

package web

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// wsConn adapts a gorilla WebSocket to a ctrlproto.FrameConn. One ctrlproto
// frame per WebSocket text message. ServeConn serializes writes, but gorilla
// also disallows concurrent writers for its own control frames, so WriteFrame
// takes a mutex as belt-and-suspenders.
type wsConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
}

// maxFrameBytes caps a single inbound WebSocket message so one oversized frame
// can't exhaust memory. Generous enough for a prompt carrying an attached image
// (raw bytes ride the frame base64-encoded), bounded enough to stop abuse.
const maxFrameBytes = 16 << 20 // 16 MiB

// A ctrlproto connection is SILENT while the agent is thinking, and a reverse
// proxy reads silence as death. haproxy applies `timeout client`/`timeout server`
// to an upgraded WebSocket unless `timeout tunnel` says otherwise, and its
// defaults are tens of seconds — so a panel sitting on a long turn had its socket
// cut out from under it on a fixed interval, forever, with no bug anywhere in
// terva that any test could see. (Observed live: a 50s haproxy timeout, and a
// panel that reconnected every 50s to the second.)
//
// So the daemon pings. Browsers answer a ping with a pong automatically — there
// is no WebSocket ping API in the page, so the server is the only end that CAN
// keep this alive — and the traffic resets the proxy's idle timer.
//
// pongWait then buys dead-peer detection for free: a client that has gone away
// without closing (a laptop that slept, a phone that lost the tailnet) stops
// answering, and the read deadline reaps the connection instead of leaking it and
// its goroutines for the life of the process.
const (
	// pingInterval is how often the daemon pings an otherwise idle socket.
	// Comfortably under any proxy idle timeout worth calling a default.
	pingInterval = 20 * time.Second
	// pongWait is how long a socket may go without ANY traffic — a pong, a
	// frame, anything — before it is considered dead. Three missed pings.
	pongWait = 65 * time.Second
)

var _ ctrlproto.FrameConn = (*wsConn)(nil)

// keepalive pings until ctx ends, so an idle socket keeps traffic on the wire and
// a peer that stopped answering is reaped rather than leaked. Runs for the life of
// one connection; the caller starts it in a goroutine.
//
// The ping shares WriteFrame's mutex because gorilla forbids concurrent writers,
// control frames included — a ping racing a streamed delta corrupts the stream.
func (w *wsConn) keepalive(ctx context.Context) { w.keepaliveEvery(ctx, pingInterval) }

// keepaliveEvery is keepalive with the interval injected, so a test can prove that
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
			_ = w.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := w.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			w.wmu.Unlock()
			if err != nil {
				return // the socket is gone; the read side will notice and tear down
			}
		}
	}
}

// armReadDeadline starts the dead-peer clock and arranges for every pong to push
// it back out. Reads refresh it too (see ReadFrame): a client that is talking is
// alive whether or not it happened to pong.
func (w *wsConn) armReadDeadline() {
	_ = w.c.SetReadDeadline(time.Now().Add(pongWait))
	w.c.SetPongHandler(func(string) error {
		return w.c.SetReadDeadline(time.Now().Add(pongWait))
	})
}

// ReadFrame blocks on the next message. Cancellation is delivered by closing
// the socket from the serveWS watcher, which makes ReadMessage return an error.
func (w *wsConn) ReadFrame(_ context.Context) (ctrlproto.Frame, error) {
	_, data, err := w.c.ReadMessage()
	if err != nil {
		return ctrlproto.Frame{}, err
	}
	// Traffic is liveness. Without this a client that talks steadily but never
	// pongs (a native peer with its own keepalive) would be reaped mid-session.
	_ = w.c.SetReadDeadline(time.Now().Add(pongWait))
	return ctrlproto.Decode(data)
}

func (w *wsConn) WriteFrame(_ context.Context, f ctrlproto.Frame) error {
	b, err := ctrlproto.Encode(f)
	if err != nil {
		return err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_ = w.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return w.c.WriteMessage(websocket.TextMessage, b)
}

func (w *wsConn) Close() error { return w.c.Close() }
