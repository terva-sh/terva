package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"terva.sh/terva/packages/lineframe"
)

// upstream is a synchronous Streamable-HTTP MCP client to one remote server. The
// bridge relays terva's requests one at a time, so — unlike core's multiplexing
// client — a plain request/response method is enough: POST a JSON-RPC frame, read
// the JSON (or SSE) reply, return it. A 401 triggers one credential renewal via
// the authSource and a single retry, which is where transparent token refresh
// happens. (The mechanics mirror packages/agent/mcp/mcp_http.go, deliberately
// re-implemented here rather than imported — the bridge takes no core dependency.)
type upstream struct {
	url     string
	hc      *http.Client
	auth    authSource
	headers map[string]string // static extra headers (operator-supplied)

	mu        sync.Mutex
	sessionID string
}

func newUpstream(remoteURL string, hc *http.Client, auth authSource, headers map[string]string) *upstream {
	return &upstream{url: remoteURL, hc: hc, auth: auth, headers: headers}
}

// call sends a JSON-RPC request and returns its result (or the server's JSON-RPC
// error). On a 401 it renews the credential once and retries — the transparent
// refresh path.
func (u *upstream) call(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) (json.RawMessage, error) {
	body, status, wwwAuth, err := u.doWithReauth(ctx, marshalRequest(id, method, params))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if status == http.StatusUnauthorized {
		return nil, fmt.Errorf("%s: upstream still unauthorized after re-auth (%s)", method, strings.TrimSpace(wwwAuth))
	}
	var resp rpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%s: unparseable upstream response: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: upstream error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (u *upstream) notify(ctx context.Context, method string, params json.RawMessage) error {
	raw, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	_, status, wwwAuth, err := u.doWithReauth(ctx, raw)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%s: upstream still unauthorized after re-auth (%s)", method, strings.TrimSpace(wwwAuth))
	}
	return nil
}

// doWithReauth sends frame, and on a 401 re-authenticates and sends it once
// more. It reports the FINAL status, so a caller can tell a send that landed
// from one that is still unauthorized.
//
// This was written twice, fifteen lines apart, and the second copy got it
// wrong. `do` returns a nil error for a 401 by design — that status is the
// re-auth signal, not a failure — so notify's hand-rolled version returned the
// second send's nil error and reported SUCCESS on a still-unauthorized send.
// The one caller is the MCP handshake, so a bridge with a dead credential
// announced a healthy connection and then failed every call after it.
//
// call's loop had a quieter version of the same hole: on the second 401 it fell
// through to json.Unmarshal, so a 401 body that happened to be valid JSON
// returned a nil result with no error, and the "still unauthorized" line at the
// bottom of the loop was unreachable.
func (u *upstream) doWithReauth(ctx context.Context, raw []byte) (body []byte, status int, wwwAuth string, err error) {
	body, status, wwwAuth, err = u.do(ctx, raw)
	if status != http.StatusUnauthorized {
		return body, status, wwwAuth, err
	}
	if rerr := u.auth.reauth(ctx); rerr != nil {
		return body, status, wwwAuth, fmt.Errorf("upstream 401 (%s) and %w", strings.TrimSpace(wwwAuth), rerr)
	}
	return u.do(ctx, raw)
}

// initialize runs the MCP handshake: the initialize request (which assigns the
// session id) followed by the initialized notification. Returns the server's
// initialize result so main can log what it connected to.
func (u *upstream) initialize(ctx context.Context) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "terva-mcp-bridge", "version": version},
	})
	res, err := u.call(ctx, json.RawMessage(`1`), "initialize", params)
	if err != nil {
		return nil, err
	}
	if err := u.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, fmt.Errorf("initialized notification: %w", err)
	}
	return res, nil
}

// do performs one POST and returns the JSON-RPC frame body, the HTTP status, and
// any WWW-Authenticate header (for the 401 path). A response may be a JSON body or
// an SSE stream; either way the JSON-RPC object is extracted. A >=400 status
// other than 401 surfaces as an error carrying the body.
func (u *upstream) do(ctx context.Context, frame []byte) (body []byte, status int, wwwAuth string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(frame))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range u.headers {
		req.Header.Set(k, v)
	}
	if h, herr := u.auth.header(ctx); herr != nil {
		return nil, 0, "", herr
	} else if h != "" {
		req.Header.Set("Authorization", h)
	}
	u.mu.Lock()
	sid := u.sessionID
	u.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := u.hc.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		u.mu.Lock()
		u.sessionID = got
		u.mu.Unlock()
	}
	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, resp.StatusCode, "", fmt.Errorf("upstream http %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	if resp.StatusCode == http.StatusAccepted {
		return nil, resp.StatusCode, "", nil // notification ack, no body
	}
	frameBody, err := readResponseFrame(resp, idOf(frame))
	return frameBody, resp.StatusCode, "", err
}

// readResponseFrame extracts the JSON-RPC object from a POST response: a JSON
// body is returned as-is; an SSE stream is scanned for the `data:` frame matching
// wantID (an MCP server may emit progress notifications before the final
// response). Falls back to the last data frame when none carries a matching id.
func readResponseFrame(resp *http.Response, wantID json.RawMessage) ([]byte, error) {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return io.ReadAll(io.LimitReader(resp.Body, bridgeMaxFrameBytes))
	}
	// lineframe's REJECT policy, and this site is why the finding called the
	// bridge the worse half. It was a raw bufio.Scanner whose Err() was NEVER
	// consulted: an over-limit `data:` line ended the loop, the function
	// returned `last` — the preceding PROGRESS NOTIFICATION — and the bridge
	// relayed a notification body to terva as the tool's result, with no error
	// anywhere. These frames are one logical response, so a skipped frame
	// corrupts the whole and must be reported rather than recovered from.
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var data bytes.Buffer
	var last []byte
	flush := func() {
		if data.Len() == 0 {
			return
		}
		frame := append([]byte(nil), data.Bytes()...)
		data.Reset()
		last = frame
	}
	for {
		raw, tooLong, err := lineframe.ReadFrame(br, bridgeMaxFrameBytes)
		if tooLong {
			return nil, fmt.Errorf("mcp server sent an event over the %d-byte frame limit; the response is incomplete", bridgeMaxFrameBytes)
		}
		line := string(lineframe.TrimCR(raw))
		switch {
		case line == "":
			flush()
			if last != nil && frameHasID(last, wantID) {
				return last, nil
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if err != nil {
			// The stream error is real and must not be swallowed: returning the
			// last frame as if it were the response is exactly the silent
			// mis-relay this reader used to perform.
			if err != io.EOF {
				return nil, err
			}
			break
		}
	}
	flush()
	if last == nil {
		return nil, fmt.Errorf("upstream SSE response carried no data frame")
	}
	return last, nil
}

func frameHasID(frame, wantID json.RawMessage) bool {
	if len(wantID) == 0 {
		return true
	}
	return bytes.Equal(idOf(frame), wantID)
}

// idOf extracts a frame's id (nil for a notification).
func idOf(frame []byte) json.RawMessage {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
		return nil
	}
	return m.ID
}
