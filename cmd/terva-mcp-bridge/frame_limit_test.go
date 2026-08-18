package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readResponseFrame was a raw bufio.Scanner whose Err() was NEVER consulted.
//
// An over-limit `data:` line made Scan() return false; the loop exited, and the
// function returned `last` — whatever frame preceded it. On a stream that opens
// with a progress notification, as MCP streams routinely do, the bridge relayed
// that NOTIFICATION to terva as the tool's result, with no error anywhere. terva
// then handed the model a progress body where a tool result belonged.
//
// The frames here are ONE logical response, so lineframe's REJECT policy is the
// right half: say the response is incomplete rather than substitute a different
// frame for it.
func TestAnOversizedSSEFrameIsAnErrorAndNotThePrecedingNotification(t *testing.T) {
	huge := strings.Repeat("x", bridgeMaxFrameBytes+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A progress notification first — the frame the old reader would have
		// returned as if it were the answer.
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		// Then the response for id 7, too large to frame.
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"blob\":%q}}\n\n", huge)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := readResponseFrame(resp, []byte(`7`))
	if err == nil {
		t.Fatalf("an over-limit response frame was accepted; got %q", truncateForMsg(frame))
	}
	if strings.Contains(string(frame), "notifications/progress") {
		t.Errorf("the preceding notification was returned as the response: %q", truncateForMsg(frame))
	}
	if !strings.Contains(err.Error(), "frame limit") {
		t.Errorf("the error does not name the cause, so a user cannot act on it: %v", err)
	}
}

// The limit must not be so eager that a legitimately large tool result — a
// screenshot, a whole-file read — is rejected. Without this the test above could
// be satisfied by a reader that refuses everything.
func TestALargeButInBoundsSSEFrameStillArrives(t *testing.T) {
	big := strings.Repeat("y", bridgeMaxFrameBytes/2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"blob\":%q}}\n\n", big)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := readResponseFrame(resp, []byte(`7`))
	if err != nil {
		t.Fatalf("a %d-byte frame under the %d-byte limit was rejected: %v", len(big), bridgeMaxFrameBytes, err)
	}
	if !strings.Contains(string(frame), `"id":7`) {
		t.Errorf("frame = %q, want the id-7 response", truncateForMsg(frame))
	}
}

func truncateForMsg(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
