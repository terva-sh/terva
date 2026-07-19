//go:build !terva_no_mcp_http

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/lineframe"
)

// This file carries the Streamable-HTTP MCP transport (spec 2025-03-26, single
// endpoint; upgrades a POST response to an SSE stream when the server streams).
// It is the live remote-MCP wire — NOT the deprecated 2024-era two-endpoint
// HTTP+SSE transport the package doc warns off. It is build-tagged (default on):
// excluding terva_no_mcp_http drops this file and makes an "http" server config
// fail with "transport not compiled in", the same graceful degrade a missing
// stdio command gets. net/http is already linked (providers), so the tag is a
// policy/code-surface knob, not a size win.
func init() { registerTransport("http", newHTTPTransport) }

// httpTransport speaks Streamable HTTP to one remote MCP endpoint. Each JSON-RPC
// message is POSTed; the response is either one JSON body or an SSE stream, and
// its frames are funnelled into Incoming() where the Client correlates them by
// id like any other transport. Request-scoped only: a tools-only client needs no
// standalone GET channel for server-initiated messages (deferred behind this
// same seam — see docs/plans/mcp-http-transport.md OQ-3).
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
	in      chan []byte

	mu          sync.Mutex
	sessionID   string
	lastEventID string // tracked from SSE `id:` for a future resumable GET channel
	closed      bool

	closeCh   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup // in-flight Send round-trips (they push to `in`)
}

func newHTTPTransport(cfg ServerConfig, _ string, _ io.Writer) (transport, error) {
	headers, err := resolveHeaders(cfg)
	if err != nil {
		return nil, err
	}
	// No client-level timeout: an SSE response stream can legitimately stay open,
	// and each request is bounded by the caller's context instead.
	return &httpTransport{
		url:     cfg.URL,
		headers: headers,
		client:  &http.Client{},
		in:      make(chan []byte, 16),
		closeCh: make(chan struct{}),
	}, nil
}

// Send dispatches one frame on its OWN goroutine and returns immediately. The
// response's frame(s) — or, on a transport failure, one synthetic error frame
// correlated to the request's id — arrive via Incoming(), so the Client's single
// wait path (its select + timeout) governs every outcome. A synchronous Send
// that read the whole response would block the caller past its timeout and let a
// hung server wedge the turn. Notifications (no id) that fail are dropped: there
// is nothing waiting to tell.
func (t *httpTransport) Send(_ context.Context, frame []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("mcp http transport is closed")
	}
	t.wg.Add(1)
	sid := t.sessionID
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()
		t.doRequest(frame, sid, idOf(frame))
	}()
	return nil
}

// doRequest performs one POST and funnels its response into Incoming(). Its
// request context is cancelled when the transport closes, so Close never blocks
// waiting on a hung server. The caller's context is deliberately NOT used to
// bound the request — the Client's per-call timeout is the wait budget, and it
// governs via the response channel, not by killing the request mid-stream.
func (t *httpTransport) doRequest(frame []byte, sid string, id json.RawMessage) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-t.closeCh:
			cancel()
		case <-stop:
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(frame))
	if err != nil {
		t.pushError(id, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		t.pushError(id, err)
		return
	}
	// The server assigns a session id on the initialize response; echo it on
	// every subsequent request (the spec's session mechanism). Captured before
	// the response body is read, so the next request already carries it.
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.mu.Lock()
		t.sessionID = got
		t.mu.Unlock()
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		t.pushError(id, fmt.Errorf("mcp http %s: %s", resp.Status, bytes.TrimSpace(body)))
		return
	}
	t.readResponse(resp, id)
}

// pushError funnels a transport failure back as a JSON-RPC error frame correlated
// to the request's id, so the waiting call fails with a real message instead of
// timing out blind. A notification (no id) has no waiter, so its failure is
// dropped.
func (t *httpTransport) pushError(id json.RawMessage, err error) {
	if len(id) == 0 {
		return
	}
	frame, merr := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -1, "message": err.Error()},
	})
	if merr != nil {
		return
	}
	t.push(frame)
}

// idOf extracts a request's id for error correlation, or nil for a notification.
func idOf(frame []byte) json.RawMessage {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil {
		return nil
	}
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return nil
	}
	return m.ID
}

// readResponse funnels a POST response's frame(s) into `in`. A JSON body is one
// frame; an SSE stream is parsed for `data:` events; an empty body (202 Accepted
// for a notification) yields nothing. reqID is the request's id (nil for a
// notification), used by the SSE path to stop once the matching response arrives.
func (t *httpTransport) readResponse(resp *http.Response, reqID json.RawMessage) {
	defer resp.Body.Close()
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.readSSE(resp.Body, reqID)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, lineframe.DefaultMaxBytes))
	if body = bytes.TrimSpace(body); len(body) > 0 {
		t.push(body)
	}
}

// readSSE parses a text/event-stream body: `data:` lines accumulate into one
// frame flushed on the blank line between events; `id:` updates lastEventID.
//
// reqID is the id of the request this stream answers. Once the response frame for
// it has been delivered, the read returns (defer-closing the body): the spec lets
// a server hold the POST stream open after the response, and this tools-only client
// has no waiter for anything further on it, so blocking in Scan until session end
// would leak one goroutine + one connection per call. A nil reqID (or a stream that
// never carries the matching id) falls back to reading to EOF as before.
func (t *httpTransport) readSSE(body io.Reader, reqID json.RawMessage) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), lineframe.DefaultMaxBytes)
	var data bytes.Buffer
	// flush emits the accumulated event and reports whether it was the response to
	// reqID — the caller's signal to stop reading.
	flush := func() bool {
		if data.Len() == 0 {
			return false
		}
		frame := make([]byte, data.Len())
		copy(frame, data.Bytes())
		data.Reset()
		t.push(frame)
		return len(reqID) > 0 && frameHasID(frame, reqID)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if flush() {
				return
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "id:"):
			id := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			t.mu.Lock()
			t.lastEventID = id
			t.mu.Unlock()
		}
		// event:, retry:, and comment (":") lines are ignored — a tools-only
		// client keys only on data.
	}
	flush() // a final event with no trailing blank line
}

// frameHasID reports whether a JSON-RPC frame carries the given request id, so the
// SSE reader can recognise the response it was waiting for. A parse miss or absent
// id is not the response (notifications and server requests carry a different id or
// none), so the reader keeps going rather than closing early on the wrong frame.
func frameHasID(frame, id json.RawMessage) bool {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil || len(m.ID) == 0 {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(m.ID), bytes.TrimSpace(id))
}

// push delivers a frame to the Client, or drops it if the transport is closing.
func (t *httpTransport) push(frame []byte) {
	select {
	case t.in <- frame:
	case <-t.closeCh:
	}
}

func (t *httpTransport) Incoming() <-chan []byte { return t.in }

// Close rejects further sends, waits for in-flight ones to finish pushing, then
// closes Incoming() (so the Client's dispatch loop ends) and best-effort deletes
// the session (the spec's teardown). Waiting for in-flight sends is what makes
// closing `in` safe with many concurrent pushers.
func (t *httpTransport) Close() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		sid := t.sessionID
		t.mu.Unlock()
		close(t.closeCh)
		t.wg.Wait()
		close(t.in)
		if sid != "" {
			go t.deleteSession(sid)
		}
	})
}

func (t *httpTransport) deleteSession(sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sid)
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if resp, err := t.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// resolveHeaders builds the per-request headers, interpolating ${ENV} and adding
// the Authorization bearer from Auth.BearerEnv. A referenced-but-unset env var is
// an error: a header that silently became empty would send a broken or anonymous
// request, and the whole point of env-only tokens is that they are present or the
// server is not reached.
func resolveHeaders(cfg ServerConfig) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg.Headers {
		iv, err := interpolateEnv(v)
		if err != nil {
			return nil, err
		}
		out[k] = iv
	}
	if be := cfg.Auth.BearerEnv; be != "" {
		tok := os.Getenv(be)
		if tok == "" {
			return nil, fmt.Errorf("mcp: bearer_env %q is not set", be)
		}
		out["Authorization"] = "Bearer " + tok
	}
	return out, nil
}

// interpolateEnv replaces every ${VAR} with os.Getenv(VAR), erroring on an unset
// var or an unterminated ${.
func interpolateEnv(s string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		b.WriteString(s[:i])
		rest := s[i+2:]
		j := strings.Index(rest, "}")
		if j < 0 {
			return "", fmt.Errorf("mcp: unterminated ${ in header value %q", s)
		}
		name := rest[:j]
		val := os.Getenv(name)
		if val == "" {
			return "", fmt.Errorf("mcp: env %q referenced in a header is not set", name)
		}
		b.WriteString(val)
		s = rest[j+1:]
	}
}
