package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport feeds a Client scripted inbound frames and records every frame
// the Client sends.
type fakeTransport struct {
	in        chan []byte
	sent      chan []byte
	closeOnce sync.Once
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{in: make(chan []byte, 16), sent: make(chan []byte, 16)}
}

func (f *fakeTransport) Send(_ context.Context, frame []byte) error {
	f.sent <- append([]byte(nil), frame...)
	return nil
}

func (f *fakeTransport) Incoming() <-chan []byte { return f.in }

func (f *fakeTransport) Close() { f.closeOnce.Do(func() { close(f.in) }) }

// TestServerRequestsGetAnswers: a ping gets an empty result under its own id,
// numeric or string. Any other method gets -32601. A notification gets nothing,
// and so does a null id, which MCP forbids on a request.
func TestServerRequestsGetAnswers(t *testing.T) {
	ft := newFakeTransport()
	cl := newClient("fake", ft, time.Minute)
	t.Cleanup(cl.Stop)

	ft.in <- []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`)
	ft.in <- []byte(`{"jsonrpc":"2.0","id":null,"method":"ping"}`)
	ft.in <- []byte(`{"jsonrpc":"2.0","id":41,"method":"ping"}`)
	ft.in <- []byte(`{"jsonrpc":"2.0","id":"s-2","method":"ping","params":{}}`)
	ft.in <- []byte(`{"jsonrpc":"2.0","id":43,"method":"sampling/createMessage","params":{}}`)

	want := map[string]string{
		`41`:    `{"jsonrpc":"2.0","id":41,"result":{}}`,
		`"s-2"`: `{"jsonrpc":"2.0","id":"s-2","result":{}}`,
		`43`:    `{"jsonrpc":"2.0","id":43,"error":{"code":-32601,"message":"Method not found"}}`,
	}
	got := map[string]string{}
	for len(got) < len(want) {
		select {
		case frame := <-ft.sent:
			var m struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(frame, &m); err != nil {
				t.Fatalf("reply is not JSON: %s", frame)
			}
			got[string(m.ID)] = string(frame)
		case <-time.After(5 * time.Second):
			t.Fatalf("server requests went unanswered: got %d of %d replies: %v", len(got), len(want), got)
		}
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("reply for id %s = %s, want %s", id, got[id], w)
		}
	}
	// The notification and the null id came first, so a reply to either
	// would have been sent by now.
	select {
	case extra := <-ft.sent:
		t.Fatalf("a notification or a null id got a reply: %s", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestServerRequestsGetAnswersOverStdio drives the stub's pingback tool: before
// it answers the call, the server sends a ping, an unsupported method under the
// call's own id, and a notification, and it waits for the replies. A client
// that answers nothing leaves the call waiting out its timeout.
func TestServerRequestsGetAnswersOverStdio(t *testing.T) {
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), TimeoutMS: 5000}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cl.Stop)

	res, err := cl.CallTool(context.Background(), "pingback", nil)
	if err != nil {
		t.Fatalf("pingback: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("pingback result = %+v", res)
	}
	// Each reply goes out from its own goroutine, so they arrive in any order.
	replies := map[string]string{}
	for _, line := range strings.Split(res.Content[0].Text, "\n") {
		var m struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("server saw a non-JSON line: %q", line)
		}
		replies[string(m.ID)] = line
	}
	if len(replies) != 2 {
		t.Fatalf("server saw %d distinct replies, want 2: %v", len(replies), replies)
	}
	if got := replies[`"srv-ping-0"`]; got != `{"jsonrpc":"2.0","id":"srv-ping-0","result":{}}` {
		t.Errorf("ping reply = %q", got)
	}
	// The unsupported method carried the call's own id. The call still
	// resolved with its real result above, so the request did not answer it.
	for id, line := range replies {
		if id == `"srv-ping-0"` {
			continue
		}
		var unknown struct {
			Error *rpcError `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &unknown); err != nil || unknown.Error == nil || unknown.Error.Code != errMethodNotFound {
			t.Errorf("unsupported-method reply = %s, want error %d", line, errMethodNotFound)
		}
	}
}

// TestRepliesInterleaveWithRequestsOverStdio writes replies and requests to one
// stdio pipe at the same time. Every frame must reach the server whole: the
// stub reports any line that is not JSON, and every add must come back right.
func TestRepliesInterleaveWithRequestsOverStdio(t *testing.T) {
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), TimeoutMS: 10000}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cl.Stop)

	const pings, callers, minCalls = 32, 8, 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	// The callers keep going until pingback returns, so request writes are in
	// flight for the whole burst of replies.
	pingbackDone := make(chan struct{})
	for g := 0; g < callers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-pingbackDone:
					if i >= minCalls {
						return
					}
				default:
				}
				res, err := cl.CallTool(context.Background(), "add", json.RawMessage(fmt.Sprintf(`{"a":%d,"b":%d}`, g, i)))
				if err != nil {
					errs <- err
					return
				}
				if want := fmt.Sprint(g + i); len(res.Content) != 1 || res.Content[0].Text != want {
					errs <- fmt.Errorf("add(%d,%d) = %+v, want %s", g, i, res, want)
					return
				}
			}
		}(g)
	}
	res, err := cl.CallTool(context.Background(), "pingback", json.RawMessage(fmt.Sprintf(`{"pings":%d}`, pings)))
	close(pingbackDone)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if err != nil {
		t.Fatalf("pingback: %v", err)
	}
	lines := strings.Split(res.Content[0].Text, "\n")
	if len(lines) != pings+1 {
		t.Fatalf("server saw %d frames, want %d: %q", len(lines), pings+1, lines)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "corrupt: ") {
			t.Errorf("a frame reached the server torn: %s", l)
		}
	}
}

// TestABurstOfServerRequestsGetsEveryReply: a server that keeps reading sends a
// burst of pings far past maxPendingReplies. The dispatch loop outruns the reply
// writes, so the slots fill although no write blocks. Every ping must still get
// its reply: a rule that dropped on full slots lost some here.
func TestABurstOfServerRequestsGetsEveryReply(t *testing.T) {
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), TimeoutMS: 10000}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cl.Stop)

	// A full slot set is a race, so one round can miss it. Several rounds make
	// a dropping rule fail all but reliably.
	const pings, rounds = 2000, 5
	args := json.RawMessage(fmt.Sprintf(`{"pings":%d,"read_while_sending":true}`, pings))
	for round := 0; round < rounds; round++ {
		res, err := cl.CallTool(context.Background(), "pingback", args)
		if err != nil {
			t.Fatalf("round %d: %v (the server waits for a reply that never came)", round, err)
		}
		if n := len(strings.Split(res.Content[0].Text, "\n")); n != pings+1 {
			t.Fatalf("round %d: server saw %d replies, want %d", round, n, pings+1)
		}
	}
}

// TestAFloodBeforeReadingKeepsTheConnection: a server writes thousands of
// requests before it reads stdin, more than the pipes can buffer either way.
// Its replies are lost, but the connection must survive and serve the next
// call. A dispatch loop that blocked until a slot freed stopped draining stdout
// here, the server blocked on stdout and never read stdin, and replyTimeout
// tore the connection down.
func TestAFloodBeforeReadingKeepsTheConnection(t *testing.T) {
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), TimeoutMS: 4000}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cl.Stop)

	// The stub waits for every reply, and some are dropped, so this call
	// times out. Its failure is the expected cost of the flood.
	if _, err := cl.CallTool(context.Background(), "pingback", json.RawMessage(`{"pings":5000}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flood call = %v, want a timeout while the connection stays up", err)
	}
	select {
	case <-cl.Done():
		t.Fatal("the flood tore the connection down")
	default:
	}
	res, err := cl.CallTool(context.Background(), "add", json.RawMessage(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("call after the flood: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "3" {
		t.Fatalf("call after the flood = %+v", res.Content)
	}
}
