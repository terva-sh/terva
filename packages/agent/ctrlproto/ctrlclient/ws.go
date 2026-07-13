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
	return dialWS(websocket.DefaultDialer, url, token)
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
	return dialWS(d, "ws://unix/ws", token)
}

func dialWS(d *websocket.Dialer, url, token string) func(ctx context.Context) (ctrlproto.FrameConn, error) {
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
		return &wsConn{c: conn}, nil
	}
}

// maxFrameBytes caps a single inbound WebSocket message, mirroring the server
// side (web/conn.go): generous enough for a snapshot carrying image payloads,
// bounded enough that one bad frame can't exhaust memory.
const maxFrameBytes = 16 << 20 // 16 MiB

// wsConn adapts a gorilla WebSocket to a ctrlproto.FrameConn — the client
// twin of web/conn.go's server wrapper. One ctrlproto frame per WebSocket
// text message. The Client serializes command writes, but gorilla also
// disallows concurrent writers for its own control frames, so WriteFrame
// takes a mutex as belt-and-suspenders.
type wsConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
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
