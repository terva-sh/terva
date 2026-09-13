package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// cancelSpyBody records how the stream releases it: Close must come from the
// reader goroutine only, after its final Read has returned.
type cancelSpyBody struct {
	pr        *io.PipeReader
	cancelled atomic.Int32
	closed    atomic.Int32
	reading   atomic.Int32
	closeMid  atomic.Int32 // Close observed while a Read was in flight
}

func (b *cancelSpyBody) Read(p []byte) (int, error) {
	b.reading.Add(1)
	defer b.reading.Add(-1)
	return b.pr.Read(p)
}

func (b *cancelSpyBody) Cancel() {
	b.cancelled.Add(1)
	_ = b.pr.CloseWithError(context.Canceled) // the transport's effect of a cancelled request
}

func (b *cancelSpyBody) Close() error {
	if b.reading.Load() > 0 {
		b.closeMid.Add(1)
	}
	b.closed.Add(1)
	return b.pr.Close()
}

// The consumer returns on the terminal frame while the reader is still parked
// waiting for the bytes that end the body. Close must release the reader by
// cancelling, and the body must be closed exactly once, by the reader, with no
// Read in flight.
func TestSSEStreamCloseCancelsInsteadOfRacingRead(t *testing.T) {
	pr, pw := io.Pipe()
	body := &cancelSpyBody{pr: pr}
	s := newSSEStream(body, "test")

	go func() { _, _ = io.WriteString(pw, "data: [DONE]\n\n") }()
	ev, ok := <-s.Events()
	if !ok || ev.Data != "[DONE]" {
		t.Fatalf("expected the terminal frame, got %+v ok=%v", ev, ok)
	}

	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: the reader was never released")
	}
	if body.cancelled.Load() != 1 {
		t.Fatalf("Cancel calls = %d, want 1", body.cancelled.Load())
	}
	if body.closed.Load() != 1 {
		t.Fatalf("Close calls = %d, want exactly 1 (from the reader)", body.closed.Load())
	}
	if body.closeMid.Load() != 0 {
		t.Fatal("body was closed while a Read was in flight")
	}
}

// A body without Cancel keeps the old behaviour: Close closes it directly.
func TestSSEStreamCloseFallsBackToBodyClose(t *testing.T) {
	pr, _ := io.Pipe()
	var closed atomic.Int32
	body := closeCounter{pr: pr, closed: &closed}
	s := newSSEStream(body, "test")
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if closed.Load() == 0 {
		t.Fatal("fallback did not close the body")
	}
}

type closeCounter struct {
	pr     *io.PipeReader
	closed *atomic.Int32
}

func (c closeCounter) Read(p []byte) (int, error) { return c.pr.Read(p) }
func (c closeCounter) Close() error               { c.closed.Add(1); return c.pr.Close() }

// doStreamWithRetry hands back a body whose Cancel aborts the request through
// its context: a Read parked on a server that never ends its body returns
// promptly once Cancel is called from another goroutine.
func TestDoStreamWithRetryBodyCancelUnparksRead(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the chunked body open, as a proxy that idles after the terminal frame does
	}))
	defer srv.Close()    // runs second: the handler has returned by then
	defer close(release) // runs first: lets the handler return

	newReq := func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	}
	resp, err := doStreamWithRetry(context.Background(), srv.Client(), newReq)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := resp.Body.(interface{ Cancel() })
	if !ok {
		t.Fatalf("body %T does not expose Cancel", resp.Body)
	}
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil { // the terminal frame
		t.Fatal(err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := resp.Body.Read(buf) // parks: the server sends nothing more
		readErr <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the Read park
	c.Cancel()
	select {
	case err := <-readErr:
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("parked Read ended with %v, want a cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not unpark the Read")
	}
	_ = resp.Body.Close()
}
