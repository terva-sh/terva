package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The Transport contract says Incoming() "is CLOSED when the transport
// disconnects (the subprocess exited, the HTTP session ended), which the Client
// reads as the server going away — it fails all pending calls and signals
// Done."
//
// The stdio half was true. The http half never was: close(t.in) happened only
// inside Close(), which is caller-initiated, so a session the SERVER ended was
// never reported. The Client kept believing it was connected and kept re-sending
// against a session id the server had forgotten, failing every call with a fresh
// 404 and never re-initializing.
//
// A 404 against a request that carried a session id is the spec's way of saying
// exactly that.
func TestARemoteSessionEndingClosesIncoming(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// The initialize response assigns the session id.
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		// The session is gone.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"session not found"}`))
	}))
	defer srv.Close()

	tr := httpTransportFor(t, srv.URL)
	defer tr.Close()

	// First send establishes the session id.
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)); err != nil {
		t.Fatal(err)
	}
	waitForFrame(t, tr.Incoming())

	// Second send is answered with 404: the session ended.
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)); err != nil {
		t.Fatal(err)
	}

	// The pending call must still fail with a real message...
	frame := waitForFrame(t, tr.Incoming())
	if len(frame) == 0 {
		t.Fatal("no error frame for the failed call")
	}

	// ...and Incoming must then CLOSE, which is the half that was missing.
	select {
	case _, ok := <-tr.Incoming():
		if ok {
			// Drain one more frame and re-check; ordering between the error
			// frame and the close is not guaranteed.
			select {
			case _, ok := <-tr.Incoming():
				if ok {
					t.Fatal("Incoming delivered a third frame and never closed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Incoming did not close after the server ended the session")
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Incoming did not close after the server ended the session — the Client still " +
			"believes it is connected and will re-send against a forgotten session id")
	}
}

// The complement: an ORDINARY error must not tear the transport down. A 500 on
// one call, or a 404 from a server that never issued a session id, is a failed
// request and nothing more — killing the transport would turn one bad call into
// a dead connection.
func TestAnOrdinaryErrorDoesNotCloseIncoming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Mcp-Session-Id is ever issued, so no session exists to end.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	tr := httpTransportFor(t, srv.URL)
	defer tr.Close()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); err != nil {
		t.Fatal(err)
	}
	waitForFrame(t, tr.Incoming())

	select {
	case _, ok := <-tr.Incoming():
		if !ok {
			t.Fatal("a 500 closed the transport; one failed call must not kill the connection")
		}
	case <-time.After(300 * time.Millisecond):
		// Still open, which is correct.
	}
}

// A 404 from a server that never assigned a session id is not a session ending
// — there was no session. Without this, keying on the status alone would tear
// down a transport that simply hit a wrong URL.
func TestA404WithNoSessionIsJustAFailedCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr := httpTransportFor(t, srv.URL)
	defer tr.Close()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); err != nil {
		t.Fatal(err)
	}
	waitForFrame(t, tr.Incoming())

	select {
	case _, ok := <-tr.Incoming():
		if !ok {
			t.Fatal("a 404 with no established session closed the transport; there was no session to end")
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func waitForFrame(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("Incoming closed before delivering the frame")
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame arrived")
		return nil
	}
}

func httpTransportFor(t *testing.T, url string) transport {
	t.Helper()
	tr, err := newHTTPTransport(ServerConfig{URL: url}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
