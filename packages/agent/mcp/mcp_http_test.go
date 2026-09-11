//go:build !terva_no_mcp_http

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTPMCP is a minimal Streamable-HTTP MCP server for tests: it answers
// initialize (assigning a session id on the response), tools/list, and
// tools/call, and records the session id echoed back on later requests. tools/
// call can answer as a single JSON body or as an SSE stream, to exercise both
// response shapes.
type fakeHTTPMCP struct {
	sse     bool          // answer tools/call as text/event-stream
	sseHold bool          // after the SSE response, hold the stream open (spec-permitted)
	held    chan struct{} // closed when a held tools/call handler unblocks (client hung up)
	auth    string        // if set, require Authorization: Bearer <auth>
	fail500 bool          // answer tools/call with HTTP 500

	mu         sync.Mutex
	sawSession []string // Mcp-Session-Id seen on non-initialize requests
	deleted    bool
}

func (f *fakeHTTPMCP) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deleted = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if f.auth != "" && r.Header.Get("Authorization") != "Bearer "+f.auth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &req)

	if req.Method != "initialize" {
		f.mu.Lock()
		f.sawSession = append(f.sawSession, r.Header.Get("Mcp-Session-Id"))
		f.mu.Unlock()
	}
	// A notification (no id) gets a bodyless 202.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if f.fail500 && req.Method == "tools/call" {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, resultFor(req.Method))
	if req.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "sess-xyz")
	}
	if f.sse && req.Method == "tools/call" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// One SSE event carrying the JSON-RPC response.
		fmt.Fprintf(w, "id: 1\nevent: message\ndata: %s\n\n", resp)
		if f.sseHold {
			// The spec lets a server keep the POST stream open after the response.
			// Hold it until the client hangs up (its context cancels), and signal
			// that — so a test can prove the transport closed the stream promptly
			// rather than leaking a goroutine + connection blocked reading it.
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
			if f.held != nil {
				close(f.held)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(resp))
}

func resultFor(method string) string {
	switch method {
	case "initialize":
		return `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}`
	case "tools/list":
		return `{"tools":[{"name":"echo","description":"echoes","inputSchema":{"type":"object"}}]}`
	case "tools/call":
		return `{"content":[{"type":"text","text":"hello from http"}],"isError":false}`
	}
	return `{}`
}

// TestHTTPTransportHandshakeAndCall drives the whole client over the HTTP
// transport against the fake server: initialize + tools/list + tools/call, and
// confirms the server-assigned session id is echoed on every later request.
func TestHTTPTransportHandshakeAndCall(t *testing.T) {
	f := &fakeHTTPMCP{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start over http: %v", err)
	}
	defer cl.Stop()

	if tools := cl.Tools(); len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want one echo", cl.Tools())
	}
	res, err := cl.CallTool(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 || res.Content[0].Text != "hello from http" {
		t.Errorf("tool result = %+v, want the http body", res)
	}

	f.mu.Lock()
	saw := append([]string(nil), f.sawSession...)
	f.mu.Unlock()
	if len(saw) == 0 {
		t.Fatal("no non-initialize requests recorded")
	}
	for _, s := range saw {
		if s != "sess-xyz" {
			t.Errorf("session id not echoed on a request: got %q, all=%v", s, saw)
		}
	}
}

// TestHTTPTransportSSEResponse: a tools/call answered as an SSE stream is parsed
// into the same result a JSON body would give.
func TestHTTPTransportSSEResponse(t *testing.T) {
	f := &fakeHTTPMCP{sse: true}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start over http: %v", err)
	}
	defer cl.Stop()

	res, err := cl.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 || res.Content[0].Text != "hello from http" {
		t.Errorf("SSE result not parsed: %+v", res)
	}
}

// TestHTTPTransportSSEStreamHeldOpenDoesNotLeak: when a server keeps the SSE POST
// stream open after emitting the response (spec-permitted), the transport must
// close it once the response arrives instead of blocking a goroutine + connection
// on it until session end. Observed via the server handler unblocking (its request
// context cancels when the client closes the connection) WITHOUT calling Stop.
func TestHTTPTransportSSEStreamHeldOpenDoesNotLeak(t *testing.T) {
	f := &fakeHTTPMCP{sse: true, sseHold: true, held: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start over http: %v", err)
	}
	defer cl.Stop()

	res, err := cl.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool over a held-open SSE stream: %v", err)
	}
	if len(res.Content) == 0 || res.Content[0].Text != "hello from http" {
		t.Errorf("SSE result not parsed: %+v", res)
	}
	// The transport must have closed the held stream after the response — NOT on
	// Stop. If it kept reading, the handler stays blocked and this times out.
	select {
	case <-f.held:
	case <-time.After(3 * time.Second):
		t.Fatal("readSSE held the SSE stream (goroutine + connection) open after the response")
	}
}

// TestHTTPTransportBearerAuth: the bearer_env token rides as Authorization, and a
// missing token fails Start cleanly (never sends an anonymous request).
func TestHTTPTransportBearerAuth(t *testing.T) {
	f := &fakeHTTPMCP{auth: "s3cret"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	cfg := ServerConfig{Transport: "http", URL: srv.URL}
	cfg.Auth.BearerEnv = "TEST_MCP_TOKEN"

	t.Setenv("TEST_MCP_TOKEN", "s3cret")
	cl, err := Start(context.Background(), "remote", cfg, "", nil)
	if err != nil {
		t.Fatalf("authed handshake failed: %v", err)
	}
	cl.Stop()

	// A bearer_env that isn't set fails before any request goes out.
	missing := ServerConfig{Transport: "http", URL: srv.URL}
	missing.Auth.BearerEnv = "DEFINITELY_UNSET_MCP_TOKEN_ZZZ"
	if _, err := Start(context.Background(), "remote", missing, "", nil); err == nil {
		t.Error("a missing bearer token must fail Start, not send an anonymous request")
	}
}

// TestHTTPTransportErrorSurfaces: an HTTP error on tools/call comes back to the
// caller as a real error (correlated to the request id), not a blind timeout —
// the point of pushError and the async Send.
func TestHTTPTransportErrorSurfaces(t *testing.T) {
	f := &fakeHTTPMCP{fail500: true}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	// Handshake succeeds (only tools/call 500s), so Start returns a live client.
	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cl.Stop()

	start := time.Now()
	_, err = cl.CallTool(context.Background(), "echo", nil)
	if err == nil {
		t.Fatal("a 500 on tools/call must surface as an error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the HTTP status, got %v", err)
	}
	// It must not have waited out the 60s tool timeout.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("error took %s — it should be prompt, not a timeout", elapsed)
	}
}

// TestHTTPTransportConfinesRedirects: the configured host is trusted (the
// user named it), but it must not be able to steer the client anywhere
// else private — a redirect to an RFC1918 target is refused by the egress
// guard, promptly, instead of being followed.
func TestHTTPTransportConfinesRedirects(t *testing.T) {
	f := &fakeHTTPMCP{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			// 307 keeps the POST + body on the hop, the worst case.
			http.Redirect(w, r, "http://10.255.255.1/mcp", http.StatusTemporaryRedirect)
			return
		}
		f.handler(w, r)
	}))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cl.Stop()

	start := time.Now()
	_, err = cl.CallTool(context.Background(), "echo", nil)
	if err == nil {
		t.Fatal("a redirect to a private address must fail the call")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Errorf("error should come from the egress guard, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("refusal took %s — the guard rejects before dialing, this should be prompt", elapsed)
	}
}

// TestHTTPTransportStripsBearerOnCrossHostRedirect: the configured bearer must
// not ride a redirect to another host. TestHTTPTransportConfinesRedirects
// covers the address policy on this same path. This covers the credential,
// which nothing covered before.
//
// It exercises the transport's OWN client rather than a guard built inside the
// test, so it fails if anyone swaps guard.Client for a bare &http.Client{} and
// quietly drops the redirect policy along with it.
//
// Why it calls CheckRedirect instead of driving a real redirect: the strip only
// fires for a target that is BOTH a different hostname and publicly routable.
// The transport allowlists exactly one host, so a cross-host redirect to a
// loopback httptest server is refused by CheckURL before the strip is reached,
// and a genuinely public target cannot be dialed in CI. CheckRedirect is what
// the transport applies to that request, and the request object is what would
// go on the wire, so asserting on it is the honest reach of this test.
//
// Every URL here is an IP literal or the allowlisted host, so CheckURL never
// calls LookupIP and the test needs no DNS.
func TestHTTPTransportStripsBearerOnCrossHostRedirect(t *testing.T) {
	t.Setenv("TEST_MCP_BEARER", "s3cr3t")
	cfg := ServerConfig{
		Transport: "http",
		URL:       "https://mcp.example.test/mcp",
		Headers:   map[string]string{"X-Workspace": "ws-1"},
	}
	cfg.Auth.BearerEnv = "TEST_MCP_BEARER"

	tr, err := newHTTPTransport(cfg, "", nil)
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	ht, ok := tr.(*httpTransport)
	if !ok {
		t.Fatalf("want *httpTransport, got %T", tr)
	}
	// Check the premise first. If the bearer never reached the headers, every
	// assertion below would pass against an empty string.
	if got := ht.headers["Authorization"]; got != "Bearer s3cr3t" {
		t.Fatalf("the bearer should be configured, got %q", got)
	}
	cr := ht.client.CheckRedirect
	if cr == nil {
		t.Fatal("the transport's client carries no redirect policy, so the egress guard is not wired into it")
	}

	mk := func(u string) *http.Request {
		req, rerr := http.NewRequest("POST", u, nil)
		if rerr != nil {
			t.Fatalf("build req %s: %v", u, rerr)
		}
		for k, v := range ht.headers {
			req.Header.Set(k, v)
		}
		return req
	}

	// A public target passes the address gate, so the strip actually runs. A
	// private target would be refused before it and prove nothing.
	cross := mk("https://1.1.1.1/steal")
	if err := cr(cross, []*http.Request{mk(cfg.URL)}); err != nil {
		t.Fatalf("a public cross-host redirect must pass the gate so the strip runs: %v", err)
	}
	if got := cross.Header.Get("Authorization"); got != "" {
		t.Errorf("the configured bearer rode a cross-host redirect: %q", got)
	}
	// The asymmetry docs/mcp.md has to warn about: only Authorization goes.
	// A custom header still carries to the new host, which is deliberate.
	if got := cross.Header.Get("X-Workspace"); got != "ws-1" {
		t.Errorf("a custom header should survive the hop, got %q", got)
	}

	// Positive control. Without it, a closure that deleted the header on every
	// hop would pass the assertion above while breaking every real redirect.
	same := mk("https://mcp.example.test/other")
	if err := cr(same, []*http.Request{mk(cfg.URL)}); err != nil {
		t.Fatalf("a same-host redirect should pass: %v", err)
	}
	if got := same.Header.Get("Authorization"); got != "Bearer s3cr3t" {
		t.Errorf("a same-host redirect must keep the bearer, got %q", got)
	}
}

// TestHTTPTransportSessionDeleteOnStop: Stop best-effort DELETEs the session.
func TestHTTPTransportSessionDeleteOnStop(t *testing.T) {
	f := &fakeHTTPMCP{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	cl, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: srv.URL}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cl.Stop()

	// The DELETE fires in a goroutine; poll briefly.
	if !waitFor(func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.deleted
	}) {
		t.Error("Stop did not DELETE the session")
	}
}

// TestInterpolateEnv covers the ${ENV} header rules.
func TestInterpolateEnv(t *testing.T) {
	t.Setenv("MCP_HDR_A", "alpha")
	if got, err := interpolateEnv("pre-${MCP_HDR_A}-post"); err != nil || got != "pre-alpha-post" {
		t.Errorf("interpolate = %q, %v; want pre-alpha-post", got, err)
	}
	if _, err := interpolateEnv("${DEFINITELY_UNSET_ZZZ}"); err == nil {
		t.Error("an unset env var must be an error, not a silent empty header")
	}
	if _, err := interpolateEnv("${UNTERMINATED"); err == nil {
		t.Error("an unterminated ${ must error")
	}
	if got, _ := interpolateEnv("no vars here"); got != "no vars here" {
		t.Errorf("plain string changed: %q", got)
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
