package ctrlclient

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// DialWebSocket returns a Dial function for [Options] that connects to a
// ctrlproto WebSocket endpoint (terva web's /ws). token, when non-empty, is
// sent as a bearer Authorization header — the native-client auth mode, kept
// out of the URL so it can't leak into logs/history the way ?token= can.
func DialWebSocket(url, token string) func(ctx context.Context) (ctrlproto.FrameConn, error) {
	return dialWS(websocket.DefaultDialer, url, token, 0)
}

// DialWebSocketReaping is DialWebSocket with a read deadline armed, so a server
// that stops answering is reaped instead of held open forever. pongWait is how
// long the socket may go without any traffic; every frame, pong, and inbound
// PING pushes it back out.
//
// It is opt-in rather than the default for DialWebSocket, and the reason is
// version skew. The deadline is only safe against a server that pings, because
// an idle ctrlproto connection carries nothing else. terva web has pinged every
// accepted socket since web/conn.go's keepalive landed, but a NEWER client
// dialling an OLDER daemon that predates it would reap a perfectly healthy
// connection every pongWait and reconnect forever. The fleet has no such past:
// its hub pings from the first release that has a hub at all.
//
// So terva attach and ctrldial keep today's behaviour, and whether they should
// adopt this is a separate decision with that compatibility question attached.
func DialWebSocketReaping(url, token string, pongWait time.Duration) func(ctx context.Context) (ctrlproto.FrameConn, error) {
	return dialWS(websocket.DefaultDialer, url, token, pongWait)
}

// DialWebSocketUnix returns a Dial function that runs the same WebSocket
// protocol over a unix domain socket (a daemon serving `--web-addr
// unix:/path`, or a systemd socket unit). The HTTP upgrade needs a URL for
// its Host header; the placeholder host is never resolved — the transport
// dial goes straight to the socket path.
func DialWebSocketUnix(path, token string) func(ctx context.Context) (ctrlproto.FrameConn, error) {
	d := &websocket.Dialer{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "unix", path)
		},
	}
	return dialWS(d, "ws://unix/ws", token, 0)
}

func dialWS(d *websocket.Dialer, url, token string, pongWait time.Duration) func(ctx context.Context) (ctrlproto.FrameConn, error) {
	return func(ctx context.Context) (ctrlproto.FrameConn, error) {
		hdr := http.Header{}
		if token != "" {
			hdr.Set("Authorization", "Bearer "+token)
		}
		conn, resp, err := d.DialContext(ctx, url, hdr)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		conn.SetReadLimit(maxFrameBytes)
		w := &wsConn{c: conn, pongWait: pongWait}
		w.armReadDeadline()
		return w, nil
	}
}

// maxFrameBytes is the protocol's ceiling, shared with the server rather than
// mirrored: a client limit below the server's silently truncates a legitimate
// snapshot into a dead connection.
const maxFrameBytes = ctrlproto.MaxFrameBytes

// writeWait bounds one write, a frame or a control frame. A peer that has gone
// away without closing otherwise parks the write forever.
const writeWait = 15 * time.Second

// wsConn adapts a gorilla WebSocket to a ctrlproto.FrameConn — the client
// twin of web/conn.go's server wrapper. One ctrlproto frame per WebSocket
// text message. The Client serializes command writes, but gorilla also
// disallows concurrent writers for its own control frames, so WriteFrame
// takes a mutex as belt-and-suspenders.
type wsConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
	// pongWait is the read deadline, or zero to keep a socket open forever.
	// See DialWebSocketReaping for why zero is still the default.
	pongWait time.Duration
}

// armReadDeadline starts the dead-server clock, when one was asked for.
//
// An inbound PING pushes the deadline, and that is the load-bearing part rather
// than a nicety. This end does not ping: it proves the path is alive by the
// server's pings arriving. An idle ctrlproto connection carries no frames at
// all, so ReadFrame never returns to push the deadline, and a handler that only
// pongs would let a healthy idle socket expire on schedule.
func (w *wsConn) armReadDeadline() {
	if w.pongWait <= 0 {
		return
	}
	_ = w.c.SetReadDeadline(time.Now().Add(w.pongWait))
	w.c.SetPongHandler(func(string) error {
		return w.c.SetReadDeadline(time.Now().Add(w.pongWait))
	})
	w.c.SetPingHandler(func(appData string) error {
		// Liveness first. The ping proves the path is up whether or not our
		// pong makes it back.
		_ = w.c.SetReadDeadline(time.Now().Add(w.pongWait))
		// Replaces gorilla's default handler, so this end still answers. A
		// failed pong returns nil on purpose: it must not tear down the read
		// loop, because a server that stops hearing from us reaps its own end
		// and our deadline covers the reverse.
		_ = w.c.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(writeWait))
		return nil
	})
}

var _ ctrlproto.FrameConn = (*wsConn)(nil)

// ReadFrame blocks on the next message. Cancellation is delivered by the
// Client closing the socket (context.AfterFunc in serve), which makes
// ReadMessage return an error — same posture as the server wrapper.
func (w *wsConn) ReadFrame(_ context.Context) (ctrlproto.Frame, error) {
	_, data, err := w.c.ReadMessage()
	if err != nil {
		return ctrlproto.Frame{}, err
	}
	// Traffic is liveness, the same rule both server wrappers use.
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
	_ = w.c.SetWriteDeadline(time.Now().Add(writeWait))
	return w.c.WriteMessage(websocket.TextMessage, b)
}

func (w *wsConn) Close() error { return w.c.Close() }
