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
}

var _ ctrlproto.FrameConn = (*wsConn)(nil)

// ReadFrame blocks on the next message. Cancellation arrives by way of the
// owner closing the socket, which makes ReadMessage return an error. That is
// the same posture both shipped wrappers take, and it is why ServeConn and
// ctrlclient both install a context.AfterFunc that closes the transport.
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
	_ = w.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	return w.c.WriteMessage(websocket.TextMessage, b)
}

func (w *wsConn) Close() error { return w.c.Close() }
