//go:build terva_web

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A ctrlproto connection is SILENT while the agent is thinking, and a reverse proxy
// reads silence as death. haproxy applies `timeout client`/`timeout server` to an
// upgraded WebSocket unless `timeout tunnel` overrides them, and the observed
// deployment cut every panel socket at 50 seconds, on the dot, forever — with no
// bug anywhere in terva that any test could see, because the daemon sent nothing at
// all on an idle connection.
//
// The page cannot fix this: there is no WebSocket ping API in a browser. The server
// is the only end that CAN keep the connection alive, so the server pings.
func TestIdleSocketIsPinged(t *testing.T) {
	// Drive the ping loop directly rather than waiting 20 real seconds for the
	// production interval — this is about whether pings are SENT on an idle socket,
	// not about the constant.
	var pings atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		conn := &wsConn{c: c}
		conn.armReadDeadline()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go conn.keepaliveEvery(ctx, 20*time.Millisecond)
		// Hold the connection open, saying nothing — the idle turn this is about.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.SetPingHandler(func(appData string) error {
		pings.Add(1)
		return c.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))
	})

	// The read pump is what dispatches control frames to the ping handler.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pings.Load() > 0 {
			return // the daemon spoke on an idle socket: the proxy's timer resets
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no ping on an idle socket — a proxy with an idle timeout (haproxy's default " +
		"applies to a WebSocket unless `timeout tunnel` says otherwise) will cut this " +
		"connection, and the browser cannot ping for itself")
}

// TestKeepaliveConstantsBeatCommonProxyTimeouts pins the numbers rather than the
// mechanism. A ping interval only helps if it is comfortably UNDER the idle timeout
// of the proxy in front — and pongWait only reaps a dead peer if it allows for
// several missed pings, or a moment's packet loss reads as death.
func TestKeepaliveConstantsBeatCommonProxyTimeouts(t *testing.T) {
	// haproxy's `timeout client` in the observed deployment. nginx defaults to 60s.
	const tightestProxyTimeout = 50 * time.Second
	if pingInterval >= tightestProxyTimeout {
		t.Errorf("pingInterval %v does not beat a %v proxy idle timeout", pingInterval, tightestProxyTimeout)
	}
	if pongWait < 3*pingInterval {
		t.Errorf("pongWait %v allows fewer than three missed pings (%v each): a blip would "+
			"reap a live client", pongWait, pingInterval)
	}
}
