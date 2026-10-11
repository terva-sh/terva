//go:build !terva_no_mcp_http

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestServerRequestsGetAnswersOverHTTP: a Streamable-HTTP server sends a ping
// on a tools/call SSE stream, ahead of the response, and waits for the reply
// POST before it answers the call. The ping carries the call's own id, as a
// server that numbers its requests from 1 will.
//
// Two traps sit on this path. The SSE reader could take the ping for the
// response it waits on and close the stream (the 202 case). A reply POST that
// fails could come back as an error under the call's id and fail the call (the
// 500 case).
func TestServerRequestsGetAnswersOverHTTP(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			replies := make(chan string, 4)
			callIDs := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusOK)
					return
				}
				body, _ := io.ReadAll(r.Body)
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				_ = json.Unmarshal(body, &req)
				switch {
				case req.Method == "":
					// The client's reply to the ping. Send the status before
					// the stream may continue, so a failure has time to land.
					w.WriteHeader(status)
					w.(http.Flusher).Flush()
					replies <- string(body)
					return
				case !hasID(req.ID):
					w.WriteHeader(http.StatusAccepted)
					return
				case req.Method == "tools/call":
					callIDs <- string(req.ID)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"method\":\"ping\"}\n\n", req.ID)
					w.(http.Flusher).Flush()
					got := "no reply"
					select {
					case got = <-replies:
					case <-time.After(3 * time.Second):
					case <-r.Context().Done():
						return
					}
					time.Sleep(200 * time.Millisecond)
					text, _ := json.Marshal(got)
					fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%s}]}}\n\n", req.ID, text)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, resultFor(req.Method))
			}))
			defer srv.Close()

			cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL, TimeoutMS: 5000}, "", nil)
			if err != nil {
				t.Fatalf("Start over http: %v", err)
			}
			defer cl.Stop()

			res, err := cl.CallTool(context.Background(), "echo", nil)
			if err != nil {
				t.Fatalf("tools/call failed: %v", err)
			}
			callID := <-callIDs
			want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, callID)
			if len(res.Content) != 1 || res.Content[0].Text != want {
				t.Fatalf("server saw reply %+v, want %s", res.Content, want)
			}
		})
	}
}

// TestAReplySendCoversItsPOST: Send of a frame with no waiter returns only when
// its POST is done, and the caller's ctx bounds it. That is what makes the
// Client's reply slots a real bound on HTTP: a slot frees when the POST ends,
// not when a goroutine for it starts.
func TestAReplySendCoversItsPOST(t *testing.T) {
	const delay = 300 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	defer close(release)
	tr := httpTransportFor(t, srv.URL)
	defer tr.Close()
	reply := []byte(`{"jsonrpc":"2.0","id":"s-1","result":{}}`)

	start := time.Now()
	if err := tr.Send(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < delay {
		t.Fatalf("Send of a reply returned after %s, before the server answered at %s", took, delay)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start = time.Now()
	_ = tr.Send(ctx, reply)
	if took := time.Since(start); took >= delay {
		t.Fatalf("Send of a reply took %s; its ctx expired at 50ms", took)
	}
}

// TestAReplyAnsweredWithAStreamIsClosedUnread: the spec answers a reply POST
// with 202 and no body. A server that answers with an event stream and holds it
// open must not hold a goroutine and a connection per reply. Nothing on that
// stream may reach the Client either: here it carries a forged response to the
// pending call.
func TestAReplyAnsweredWithAStreamIsClosedUnread(t *testing.T) {
	callIDs := make(chan string, 1)
	hungUp := make(chan struct{}) // closed when the client hangs up the reply stream
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		switch {
		case req.Method == "":
			// The reply to the ping: answer with a held stream that forges the
			// response to the pending call.
			callID := <-callIDs
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"forged\"}]}}\n\n", callID)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(hungUp)
			return
		case !hasID(req.ID):
			w.WriteHeader(http.StatusAccepted)
			return
		case req.Method == "tools/call":
			callIDs <- string(req.ID)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"p1\",\"method\":\"ping\"}\n\n")
			w.(http.Flusher).Flush()
			text := "the reply stream stayed open"
			select {
			case <-hungUp:
				text = "the reply stream was closed"
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n\n", req.ID, text)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, resultFor(req.Method))
	}))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL, TimeoutMS: 5000}, "", nil)
	if err != nil {
		t.Fatalf("Start over http: %v", err)
	}
	defer cl.Stop()

	res, err := cl.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "the reply stream was closed" {
		t.Fatalf("tools/call result = %+v, want the server's own answer after the reply stream closed", res.Content)
	}
}
