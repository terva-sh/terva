package ctrlclient

// The unix-socket transport: DialWebSocketUnix against the real ServeConn
// behind a plain http.Server on a filesystem socket — the client half of a
// daemon serving `--web-addr unix:/path` or a systemd socket unit.

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

func TestWebSocketOverUnixSocket(t *testing.T) {
	sock := filepath.Join(testsupport.TempDir(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer ln.Close()

	svc := &fakeSvc{}
	upgrad := websocket.Upgrader{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, uerr := upgrad.Upgrade(w, r, nil)
		if uerr != nil {
			return
		}
		fc := &wsConn{c: c}
		_, _ = ctrlproto.ServeConn(r.Context(), fc, svc, ctrlproto.ServerHello("unix-daemon", "9.9.9"))
		_ = fc.Close()
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client, err := New(Options{Dial: DialWebSocketUnix(sock, "")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer client.Close()
	go func() { _ = client.Run(ctx) }()
	waitFor(t, "unix connect", client.Connected)

	if hello, ok := client.ServerHello(); !ok || hello.Version != "9.9.9" {
		t.Fatalf("server hello over unix = %+v ok=%v", hello, ok)
	}
	if err := client.Service().Prompt(context.Background(), "s1", ctrlproto.PromptParams{Text: "over-unix"}); err != nil {
		t.Fatalf("Prompt over unix socket: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.prompts) != 1 || svc.prompts[0] != "s1:over-unix" {
		t.Fatalf("prompts = %v", svc.prompts)
	}
}
