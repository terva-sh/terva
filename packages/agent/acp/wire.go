//go:build terva_acp

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"terva.sh/terva/packages/lineframe"
)

// JSON-RPC 2.0 error codes. The first four are standard; auth_required and
// resource_not_found are ACP-specific (§13).
const (
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternalError    = -32603
	CodeAuthRequired     = -32000
	CodeResourceNotFound = -32002
)

// rpcError is a JSON-RPC 2.0 error object. It doubles as a Go error so a
// handler can `return nil, &rpcError{...}` and the wire layer maps it onto
// the response frame verbatim.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

func errMethodNotFound(method string) *rpcError {
	return &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

func errInvalidParams(msg string) *rpcError {
	return &rpcError{Code: CodeInvalidParams, Message: "invalid params: " + msg}
}

func errInternal(msg string) *rpcError {
	return &rpcError{Code: CodeInternalError, Message: msg}
}

// rpcMessage is the union read off the wire. A request has both Method and
// ID; a notification has Method and no ID; a response has an ID and a
// Result or Error. We disambiguate on presence after decode.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handlerFunc handles one inbound request/notification. A nil error with a
// nil result is valid (e.g. notifications). Returning an *rpcError maps to
// a JSON-RPC error response; any other error becomes CodeInternalError. The
// optional `after` callback runs once the response has been written, so a
// handler can emit a follow-up notification that must NOT precede its own
// response — e.g. available_commands_update, which a client drops if it
// arrives before the session/new response that first reveals the sessionId.
type handlerFunc func(ctx context.Context, method string, params json.RawMessage) (result any, after func(), err error)

// conn is the hand-rolled JSON-RPC 2.0 transport over newline-delimited
// JSON on a single reader/writer pair (stdin/stdout in production, an
// io.Pipe pair in tests). It mirrors rpcServer's single-writer discipline:
// every outbound frame goes through write() under writeMu so streamed
// session/update notifications and request frames can never interleave.
type conn struct {
	r       *lineframe.Reader
	w       io.Writer
	handler handlerFunc

	writeMu sync.Mutex

	// inFlight tracks dispatched handler goroutines so run() waits for any
	// turn that's mid-flush before returning when the reader closes — the
	// same discipline as rpcServer.inFlight, so a client that sends one
	// request and disconnects still gets the full response.
	inFlight sync.WaitGroup

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMessage
}

// acpMaxFrameBytes bounds one inbound JSON-RPC frame. ACP prompt and result
// payloads run large, so this sits well above lineframe's 4 MiB default.
const acpMaxFrameBytes = 16 << 20 // 16 MiB

func newConn(r io.Reader, w io.Writer, h handlerFunc) *conn {
	return &conn{
		// lineframe skips an over-limit frame instead of dying the way
		// bufio.Scanner does, so one oversized frame drops a single request
		// rather than tearing down the whole session.
		r:       lineframe.NewReader(r, acpMaxFrameBytes, nil),
		w:       w,
		handler: h,
		pending: make(map[int64]chan rpcMessage),
	}
}

// run reads frames until the reader closes. Each inbound request/notification
// is dispatched on its own goroutine so a long-running session/prompt does
// not block the read loop (which must stay live to receive session/cancel
// and to deliver responses to agent-initiated requests).
func (c *conn) run(ctx context.Context) error {
	var readErr error
	for {
		line, err := c.r.Read()
		if err != nil {
			if err != io.EOF {
				readErr = err // a clean EOF stays nil, matching Scanner.Err
			}
			break
		}
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.writeError(nil, &rpcError{Code: CodeInvalidRequest, Message: "parse error: " + err.Error()})
			continue
		}
		switch {
		case msg.Method != "":
			// Request (has id) or notification (no id).
			id := msg.ID
			params := msg.Params
			method := msg.Method
			c.inFlight.Add(1)
			go func() {
				defer c.inFlight.Done()
				c.serve(ctx, method, id, params)
			}()
		case len(msg.ID) > 0:
			// Response to a request we sent (e.g. session/request_permission).
			c.deliver(msg)
		}
	}
	c.inFlight.Wait()
	return readErr
}

// serve runs one handler call and writes its response (for requests; a
// notification carries no id, so we suppress the response frame).
func (c *conn) serve(ctx context.Context, method string, id, params json.RawMessage) {
	result, after, err := c.handler(ctx, method, params)
	if len(id) != 0 { // request — write the response (notifications carry no id)
		if err != nil {
			c.writeError(id, asRPCError(err))
			return // error path: skip any post-response action
		}
		c.writeResult(id, result)
	}
	// Runs after the response is on the wire, so a follow-up notification
	// can't race ahead of it (see handlerFunc).
	if after != nil {
		after()
	}
}

func asRPCError(err error) *rpcError {
	if re, ok := err.(*rpcError); ok {
		return re
	}
	return errInternal(err.Error())
}

// notify sends a one-way notification (no id, no response expected). Used
// for session/update.
func (c *conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

// request sends an agent->client request and blocks until the matching
// response arrives or ctx is cancelled. Used by the Phase 2 permission
// flow; present now so the seam is real, not stubbed.
func (c *conn) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	idRaw, _ := json.Marshal(id)
	if err := c.write(rpcMessage{JSONRPC: "2.0", ID: idRaw, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// deliver routes a response frame to the goroutine blocked in request().
func (c *conn) deliver(msg rpcMessage) {
	var id int64
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	c.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (c *conn) writeResult(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		c.writeError(id, errInternal(err.Error()))
		return
	}
	_ = c.write(rpcMessage{JSONRPC: "2.0", ID: id, Result: raw})
}

func (c *conn) writeError(id json.RawMessage, e *rpcError) {
	// A null id is valid for errors that can't be correlated (parse error).
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	_ = c.write(rpcMessage{JSONRPC: "2.0", ID: id, Error: e})
}

// write is the single-writer choke point: one frame per line, mutex-guarded
// so concurrent goroutines (the read loop's dispatched handlers, the prompt
// turn's session/update emits, agent->client requests) can't interleave
// bytes on the wire.
func (c *conn) write(msg rpcMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	_, err = c.w.Write([]byte("\n"))
	return err
}
