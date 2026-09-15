//go:build terva_web

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/authz"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tenant"
)

// TenantResolver turns an authenticated principal into the environment it may
// use, spawning one if the supervisor's policy says so. It is supplied by the
// composition root rather than reached for here: whether an authenticated
// stranger gets an environment is D7's question, and answering it inside a
// transport handler is how "authenticated" quietly becomes "entitled".
type TenantResolver func(context.Context, authz.Principal) (*tenant.Child, error)

// proxyWS carries one browser connection to that principal's child.
//
// The supervisor speaks the protocol rather than piping bytes blindly, because
// the gate has to see every command frame — but it forwards the ORIGINAL bytes
// of everything it passes. Re-marshalling from a parsed Frame would silently
// drop any field this build does not know about, which turns a version skew
// between supervisor and child into data loss that looks like a bug in neither.
// Only the server hello is rewritten, because narrowing it is the point.
//
// carrier says what THIS supervisor serves over HTTP, which is not what the
// child believes it serves — see tenant.Carrier.
func proxyWS(ctx context.Context, resolve TenantResolver, carrier tenant.Carrier, w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFrom(r)
	child, ok := resolveForRequest(w, r, resolve)
	if !ok {
		return
	}
	// Held for the life of the connection, so the idle reaper cannot stop a
	// child somebody is attached to. "Idle" has to mean "nobody is attached",
	// not "nobody has typed recently", or a long think would cost a websocket.
	defer child.Hold()()

	// Dial the child BEFORE upgrading. A failure here is an HTTP status the
	// browser can act on; after the upgrade it is a close frame with no status,
	// which the panel cannot tell from the machine going to sleep.
	upstream, err := dialChild(ctx, child)
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva serve: dial %s: %v\n", child.ID, err)
		http.Error(w, "the environment is not answering", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	down.SetReadLimit(maxFrameBytes)
	upstream.SetReadLimit(maxFrameBytes)
	defer down.Close()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		_ = down.Close()
		_ = upstream.Close()
	}()

	start := time.Now()
	fmt.Fprintf(os.Stderr, "terva serve: %s connected to environment %s\n", principal.Subject, child.ID)
	defer func() {
		fmt.Fprintf(os.Stderr, "terva serve: %s left environment %s (%s)\n",
			principal.Subject, child.ID, time.Since(start).Round(time.Second))
	}()

	// Both pumps write downstream — one forwards the child's frames, the other
	// answers refusals the child never sees — and gorilla allows exactly one
	// writer at a time. The mutex is shared rather than each pump holding its
	// own, which is the only arrangement that actually serialises them.
	toBrowser := &lockedConn{c: down}

	errs := make(chan error, 2)
	go func() { errs <- pumpToChild(down, upstream, toBrowser, principal) }()
	go func() { errs <- pumpToBrowser(upstream, toBrowser, principal, carrier) }()
	<-errs
}

// lockedConn serialises writes to one websocket. Reads are not guarded: each
// direction has exactly one reader.
type lockedConn struct {
	mu sync.Mutex
	c  *websocket.Conn
}

func (l *lockedConn) writeText(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c.WriteMessage(websocket.TextMessage, b)
}

func (l *lockedConn) writeFrame(f ctrlproto.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return l.writeText(b)
}

// dialChild opens the websocket to a tenant's daemon over its private socket.
// No token: the socket file's permissions are the boundary, exactly as they are
// for `terva web --web-addr unix:...` today.
func dialChild(ctx context.Context, child *tenant.Child) (*websocket.Conn, error) {
	d := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "unix", child.Socket)
		},
	}
	c, resp, err := d.DialContext(ctx, "ws://unix/ws", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return c, err
}

// pumpToChild forwards the browser's frames, refusing the ones this principal
// may not send. A refusal is answered HERE and never forwarded: the child would
// have served it, because on its own socket the only caller is the supervisor
// and it trusts what reaches it.
func pumpToChild(down, up *websocket.Conn, toBrowser *lockedConn, p authz.Principal) error {
	for {
		typ, raw, err := down.ReadMessage()
		if err != nil {
			return err
		}
		if typ != websocket.TextMessage {
			continue
		}
		var f ctrlproto.Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			// Unparseable: refused rather than forwarded. The supervisor cannot
			// gate what it cannot read, and forwarding it would mean the one
			// frame that skipped the gate is the malformed one.
			_ = toBrowser.writeFrame(ctrlproto.ErrFrame(0, ctrlproto.CodeBadRequest, "unparseable frame"))
			continue
		}
		if refusal := tenant.CheckCommand(f, p); refusal != nil {
			if err := toBrowser.writeFrame(*refusal); err != nil {
				return err
			}
			continue
		}
		if err := up.WriteMessage(websocket.TextMessage, raw); err != nil {
			return err
		}
	}
}

// pumpToBrowser forwards the child's frames, narrowing the one that says what
// this connection may do — and what this HOST actually serves.
func pumpToBrowser(up *websocket.Conn, down *lockedConn, p authz.Principal, carrier tenant.Carrier) error {
	for {
		typ, raw, err := up.ReadMessage()
		if err != nil {
			return err
		}
		if typ != websocket.TextMessage {
			continue
		}
		// Only a hello is rewritten, and only after confirming it is one —
		// everything else crosses as the bytes the child wrote.
		var probe struct {
			Kind ctrlproto.Kind `json:"kind"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil && probe.Kind == ctrlproto.KindHello {
			var f ctrlproto.Frame
			if err := json.Unmarshal(raw, &f); err == nil && f.Hello != nil {
				narrowed := tenant.NarrowHello(*f.Hello, p, carrier)
				f.Hello = &narrowed
				if err := down.writeFrame(f); err != nil {
					return err
				}
				continue
			}
		}
		if err := down.writeText(raw); err != nil {
			return err
		}
	}
}
