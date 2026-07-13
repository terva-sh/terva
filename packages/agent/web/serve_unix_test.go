//go:build terva_web

package web

// The unix-socket listener: address-form parsing, the 0600 posture, stale
// vs live socket handling, and the systemd env gate. The end-to-end request
// path over a socket is exercised through Serve itself.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func TestUnixSocketPathForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:8730", ""},
		{"unix:/run/terva.sock", "/run/terva.sock"},
		{"unix:///run/terva.sock", "/run/terva.sock"},
		{"unix:./rel.sock", "./rel.sock"},
	}
	for _, c := range cases {
		if got := unixSocketPath(c.in); got != c.want {
			t.Errorf("unixSocketPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Serve on a unix: address answers requests over the socket, owner-only.
func TestServeOnUnixSocket(t *testing.T) {
	sock := filepath.Join(testsupport.TempDir(t), "d.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, newFakeWS(), Options{Addr: "unix:" + sock})
	}()

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err = httpc.Get("http://terva/healthz")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("healthz over unix socket: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	// No-auth over the socket is allowed — the file permissions gate it —
	// so an authed route must answer too (hostAllowed would refuse the
	// placeholder Host on a TCP no-auth bind).
	resp, err = httpc.Get("http://terva/")
	if err != nil {
		t.Fatalf("PWA root over unix socket: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PWA root = %d, want 200 (unix listener bypasses the loopback-Host gate)", resp.StatusCode)
	}

	if runtime.GOOS != "windows" {
		fi, serr := os.Stat(sock)
		if serr != nil {
			t.Fatalf("stat socket: %v", serr)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("socket perms = %o, want 0600", perm)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

// A crashed daemon's leftover socket is cleared; a live daemon's is not.
func TestListenUnixStaleAndLive(t *testing.T) {
	dir := testsupport.TempDir(t)

	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	// Simulate a crash: the file outlives the listener. Close unlinks on
	// most platforms, so re-create the file to model the leftover.
	_ = ln.Close()
	if _, err := os.Stat(stale); os.IsNotExist(err) {
		if f, ferr := os.Create(stale); ferr == nil {
			f.Close()
		}
	}
	ln2, err := listenUnix(stale)
	if err != nil {
		t.Fatalf("listenUnix over stale socket: %v", err)
	}
	_ = ln2.Close()

	live := filepath.Join(dir, "live.sock")
	ln3, err := net.Listen("unix", live)
	if err != nil {
		t.Fatalf("listen live: %v", err)
	}
	defer ln3.Close()
	go func() {
		for {
			c, aerr := ln3.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := listenUnix(live); err == nil {
		t.Fatal("listenUnix stole a live daemon's socket")
	}
}

// The systemd gate: no env → not activated; someone else's activation →
// ignored; more than one fd → refused with instructions.
func TestSystemdListenerEnvGate(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	t.Setenv("LISTEN_PID", "")
	if ln, err := systemdListener(); ln != nil || err != nil {
		t.Fatalf("no env: ln=%v err=%v", ln, err)
	}

	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_PID", "1") // not us
	if ln, err := systemdListener(); ln != nil || err != nil {
		t.Fatalf("foreign pid: ln=%v err=%v", ln, err)
	}

	t.Setenv("LISTEN_PID", fmt.Sprint(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := systemdListener(); err == nil {
		t.Fatal("two fds should be refused")
	}
}
