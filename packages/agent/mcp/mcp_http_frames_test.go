//go:build !terva_no_mcp_http

package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/lineframe"
)

// readSSE used to be a raw bufio.Scanner whose Err() was never read. The
// consequences compound, and the second is the one that made this a high:
//
//  1. An over-limit `data:` line makes Scan() return false, the loop exits, and
//     readSSE returns as if the stream ended cleanly — so the pending tools/call
//     waits out its 60s timeout and fails naming nothing.
//  2. A Scanner is PERMANENTLY done after ErrTooLong. A valid response frame
//     emitted AFTER the oversized one never arrives either: the whole remainder
//     of the stream is lost.
//
// That is exactly the tear-down lineframe's RECOVER policy exists to prevent,
// and this pins the recovery rather than merely the bound.
func TestAnOversizedSSEFrameDoesNotKillTheRestOfTheStream(t *testing.T) {
	huge := strings.Repeat("x", lineframe.DefaultMaxBytes+1024)
	var stderr bytes.Buffer
	tr := &httpTransport{
		in:      make(chan []byte, 4),
		closeCh: make(chan struct{}),
		stderr:  &stderr,
	}
	defer close(tr.closeCh)

	var body bytes.Buffer
	// An oversized frame, then a perfectly good response for id 7.
	body.WriteString("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":6,\"result\":{\"blob\":\"" + huge + "\"}}\n\n")
	body.WriteString("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.readSSE(&body, json.RawMessage(`7`))
	}()

	select {
	case frame := <-tr.in:
		if !strings.Contains(string(frame), `"id":7`) {
			t.Errorf("first delivered frame = %q, want the id-7 response that follows the oversized one", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived after an oversized one — the reader is dead for the rest of the stream, " +
			"which is the tear-down lineframe's RECOVER policy exists to prevent")
	}
	<-done

	// The drop must be REPORTED. Silence is how the original defect stayed
	// invisible: the failure looked like a server that never answered.
	if !strings.Contains(stderr.String(), "dropped a frame") {
		t.Errorf("the skipped frame was not reported to the server's log, so the cause is undiagnosable: %q", stderr.String())
	}
}

// The complement: a frame comfortably under the ceiling must still arrive whole.
// Without this the test above could be satisfied by a reader that drops
// everything.
func TestALargeButInBoundsSSEFrameArrivesIntact(t *testing.T) {
	big := strings.Repeat("y", lineframe.DefaultMaxBytes/2)
	tr := &httpTransport{in: make(chan []byte, 4), closeCh: make(chan struct{})}
	defer close(tr.closeCh)

	var body bytes.Buffer
	body.WriteString("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"blob\":\"" + big + "\"}}\n\n")

	go tr.readSSE(&body, json.RawMessage(`7`))
	select {
	case frame := <-tr.in:
		if !bytes.Contains(frame, []byte(big)) {
			t.Errorf("a %d-byte payload under the %d-byte limit did not survive", len(big), lineframe.DefaultMaxBytes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an in-bounds frame never arrived")
	}
}

// A multi-line SSE event (several `data:` lines for one payload) must still
// concatenate. The Scanner did this by accident of line-at-a-time reading; the
// replacement has to do it on purpose.
func TestAMultiLineSSEEventStillConcatenates(t *testing.T) {
	tr := &httpTransport{in: make(chan []byte, 4), closeCh: make(chan struct{})}
	defer close(tr.closeCh)

	var body bytes.Buffer
	body.WriteString("event: message\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":7,\"result\":{\"ok\":true}}\n\n")

	go tr.readSSE(&body, json.RawMessage(`7`))
	select {
	case frame := <-tr.in:
		var m map[string]any
		if err := json.Unmarshal(bytes.ReplaceAll(frame, []byte("\n"), nil), &m); err != nil {
			t.Fatalf("the reassembled event is not valid JSON (a lost CRLF trim would do this): %q", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a multi-line event never arrived")
	}
}
