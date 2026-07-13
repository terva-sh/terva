package ctrlclient

// End-to-end over a real WebSocket: the DialWebSocket transport against an
// httptest server running the real ServeConn — bearer-token header, command
// round-trip, and reconnection after a server-side kill.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func TestWebSocketRoundTripAndReconnect(t *testing.T) {
	var (
		mu     sync.Mutex
		auths  []string
		conns  []*websocket.Conn
		upgrad = websocket.Upgrader{}
	)
	svc := &fakeSvc{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		c, err := upgrad.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
		fc := &wsConn{c: c}
		_, _ = ctrlproto.ServeConn(r.Context(), fc, svc, ctrlproto.ServerHello("ws-daemon", "1.2.3"))
		_ = fc.Close()
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	var connects int
	var cmu sync.Mutex
	client, err := New(Options{
		Dial:    DialWebSocket(wsURL, "sekrit"),
		Backoff: 20 * time.Millisecond,
		OnConnect: func(ctrlproto.Hello) {
			cmu.Lock()
			connects++
			cmu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer client.Close()
	go func() { _ = client.Run(ctx) }()
	waitFor(t, "ws connect", client.Connected)

	mu.Lock()
	gotAuth := auths[0]
	mu.Unlock()
	if gotAuth != "Bearer sekrit" {
		t.Fatalf("Authorization = %q, want Bearer sekrit", gotAuth)
	}
	if hello, ok := client.ServerHello(); !ok || hello.Version != "1.2.3" {
		t.Fatalf("server hello = %+v ok=%v", hello, ok)
	}
	if err := client.Service().Prompt(context.Background(), "s1", "over-ws", nil); err != nil {
		t.Fatalf("Prompt over ws: %v", err)
	}

	// Kill the connection server-side; the client must come back by itself.
	mu.Lock()
	_ = conns[len(conns)-1].Close()
	mu.Unlock()
	waitFor(t, "ws reconnect", func() bool {
		cmu.Lock()
		defer cmu.Unlock()
		return connects >= 2 && client.Connected()
	})
	if err := client.Service().Prompt(context.Background(), "s1", "after-reconnect", nil); err != nil {
		t.Fatalf("Prompt after reconnect: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.prompts) != 2 || svc.prompts[1] != "s1:after-reconnect" {
		t.Fatalf("prompts = %v", svc.prompts)
	}
}
