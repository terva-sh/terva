package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The deadlock itself, against a REAL transport.
//
// sse_close_race_test.go next door pins the CONTRACT — Close never races a
// Read, the reader owns the body — through a fake body it can observe. That is
// the right shape for a contract, and it cannot reproduce this, because the
// thing that deadlocked was net/http: no hand-written io.ReadCloser has a
// bodyEOFSignal inside it.
//
// 🪤 Getting the trigger right took two tries, and the wrong one is the
// instructive half. A server that stalls MID-STREAM, holding the body open
// while the reader waits for bytes, does not deadlock at all — the old
// body.Close unparks that reader cleanly, and a test built on it passes just as
// well before the fix as after. Written as a regression test it would have been
// decoration.
//
// The real trigger is the END of the body: the reader reaches EOF, enters the
// HTTP/1 end-of-body handshake, and Close lands on top of it. That is the state
// the CI goroutine dump caught — bodyEOFSignal.condfn, parked — and it is the
// ~90s post-turn stall behind a keep-alive proxy. Closing the body there leaves
// the handshake half finished and the reader never returns, so the drain in
// Close waits on a channel that will never close.
//
// Verified in both directions, which is the only reason to trust it: it fails
// on the first iteration against the tree before the fix, and passes with it.
func TestSSECloseRacesTheEndOfTheBody(t *testing.T) {
	// The race needs no coaxing once it is aimed correctly — it lands
	// immediately — but the iterations cost nothing when the fix is in and they
	// keep a rarer scheduling from hiding a regression.
	const iterations = 50

	for i := range iterations {
		endBody := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, ok := w.(http.Flusher)
			if !ok {
				return
			}
			_, _ = w.Write([]byte("data: {\"hello\":\"world\"}\n\n"))
			fl.Flush()
			<-endBody // returning from the handler is what ends the body
		}))

		ctx, cancel := context.WithCancel(context.Background())
		resp, err := doStreamWithRetry(ctx, http.DefaultClient, func() (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		})
		if err != nil {
			cancel()
			srv.Close()
			t.Fatalf("iteration %d: open the stream: %v", i, err)
		}
		s := newSSEStream(resp.Body, "test")

		// Take the event, so the reader is on its next Read — one beat from the
		// EOF the server is about to hand it.
		if _, ok := <-s.Events(); !ok {
			cancel()
			srv.Close()
			t.Fatalf("iteration %d: stream ended before delivering its event", i)
		}

		// End the body and close the stream at the same instant.
		closed := make(chan struct{})
		go close(endBody)
		go func() {
			s.Close()
			close(closed)
		}()

		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: Close never returned. The reader is parked in "+
				"net/http's end-of-body handshake, so it never closes the event "+
				"channel, and the drain inside Close waits for it forever. In "+
				"production this holds a turn open until the server drops the "+
				"connection — about 90 seconds behind a keep-alive proxy.", i)
		}
		cancel()
		srv.Close()
	}
}

// The ordinary ending must not pay for the fix.
//
// Close waits out a grace period before it cancels, so that a stream finishing
// on its own is not aborted and its pooled connection is not churned. A reader
// that has already finished must not sit through that wait — if it does, every
// healthy turn gets slower by the length of the grace period, which is the kind
// of regression that hides as "feels sluggish" rather than as a failure.
func TestSSECloseIsPromptWhenTheServerEndsNormally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"hello\":\"world\"}\n\n"))
		// Returning ends the body, which is what a well-behaved server does.
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, err := doStreamWithRetry(ctx, http.DefaultClient, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	s := newSSEStream(resp.Body, "test")
	for range s.Events() { //nolint:revive // read to the end, as a client does
	}

	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed >= sseCloseGrace {
		t.Errorf("Close took %v on a stream whose reader had already finished; it "+
			"must not wait out the %v grace period, or every healthy turn pays it",
			elapsed, sseCloseGrace)
	}
}
