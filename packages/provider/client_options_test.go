package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// streamAnthropic runs one request and returns the body the server received.
func streamAnthropic(t *testing.T, opts ...ClientOption) []byte {
	t.Helper()
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	ch, err := NewAnthropic("k", srv.URL, opts...).Stream(context.Background(), Request{
		Model:    "claude-sonnet-4.5",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive // drain
	}
	return sent
}

// The dump receives exactly the body the wire sends, and without the option the
// wire has nowhere to write one: it opens no file of its own.
func TestTheRequestDumpGetsTheBodySent(t *testing.T) {
	var got [][]byte
	sent := streamAnthropic(t, WithRequestDump(func(b []byte) { got = append(got, append([]byte(nil), b...)) }))
	if len(got) != 1 || !bytes.Equal(got[0], sent) {
		t.Fatalf("dump got %d bodies %q; the server received %q", len(got), got, sent)
	}
	streamAnthropic(t) // no option: nothing to call, nothing to fail
}
