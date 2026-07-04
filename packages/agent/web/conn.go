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

var _ ctrlproto.FrameConn = (*wsConn)(nil)

// ReadFrame blocks on the next message. Cancellation is delivered by closing
// the socket from the serveWS watcher, which makes ReadMessage return an error.
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
