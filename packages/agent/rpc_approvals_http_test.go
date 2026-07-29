//go:build !terva_no_mcp_http

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeApprovalHTTP is a minimal Streamable-HTTP MCP server that plays the role a
// REMOTE orchestrator plays for --approval-http: it exposes one permission tool
// and answers tools/call with a {behavior} verdict — the identical contract our
// local mcpbridge serves and a Claude worker's --permission-prompt-tool speaks.
// It records what the worker asked (tool name + arguments) so a test can prove
// the confirmation actually crossed the wire carrying its payload.
type fakeApprovalHTTP struct {
	toolName string // the permission tool this endpoint exposes
	verdict  string // the raw {behavior...} JSON returned as the tool result
	wantAuth string // if set, require Authorization: Bearer <wantAuth>

	mu       sync.Mutex
	calledAs string          // params.name seen on the tools/call
	gotArgs  json.RawMessage // params.arguments seen on the tools/call
	sawAuth  string          // Authorization header seen on the tools/call
}

func (f *fakeApprovalHTTP) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)

	// A notification (no id) is acked bodylessly.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result string
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-approval")
		result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"orch","version":"1"}}`
	case "tools/list":
		result = fmt.Sprintf(`{"tools":[{"name":%q,"description":"gate","inputSchema":{"type":"object"}}]}`, f.toolName)
	case "tools/call":
		if f.wantAuth != "" && r.Header.Get("Authorization") != "Bearer "+f.wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.calledAs = req.Params.Name
		f.gotArgs = append(json.RawMessage(nil), req.Params.Arguments...)
		f.sawAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		// The verdict rides as the tool result's text content — where the
		// confirmer reads {behavior} from.
		result = fmt.Sprintf(`{"content":[{"type":"text","text":%q}],"isError":false}`, f.verdict)
	default:
		result = `{}`
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, result)
}

func (f *fakeApprovalHTTP) snapshot() (string, json.RawMessage, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calledAs, f.gotArgs, f.sawAuth
}

// TestApprovalHTTPAllow is the payoff of the Streamable-HTTP transport, end to
// end: a worker's confirmation routes over HTTP to a remote endpoint that
// answers allow, and the worker's gate opens — carrying the tool name and the
// rendered preview across the wire (the endpoint has no other way to know them).
func TestApprovalHTTPAllow(t *testing.T) {
	f := &fakeApprovalHTTP{toolName: "approval_prompt", verdict: `{"behavior":"allow"}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	spec := fmt.Sprintf(`{"url":%q}`, srv.URL) // tool defaults to approval_prompt
	confirmer, stop, err := startHTTPConfirmer(context.Background(), spec, "")
	if err != nil {
		t.Fatalf("startHTTPConfirmer: %v", err)
	}
	defer stop()

	d := confirmer.Confirm(context.Background(), "Write", "worker w-7: write app.txt")
	if !d.Allow {
		t.Fatalf("remote allow did not open the gate: %+v", d)
	}

	calledAs, gotArgs, _ := f.snapshot()
	if calledAs != "approval_prompt" {
		t.Errorf("endpoint tool = %q, want the default approval_prompt", calledAs)
	}
	var args struct {
		ToolName string `json:"tool_name"`
		Preview  string `json:"preview"`
	}
	if err := json.Unmarshal(gotArgs, &args); err != nil {
		t.Fatalf("endpoint got unparseable args %q: %v", gotArgs, err)
	}
	if args.ToolName != "Write" || args.Preview != "worker w-7: write app.txt" {
		t.Errorf("endpoint got tool_name=%q preview=%q, want the worker's tool + rendered preview", args.ToolName, args.Preview)
	}
}

// TestApprovalHTTPDeny: a remote deny keeps the gate shut and surfaces the
// orchestrator's message as the decision reason.
func TestApprovalHTTPDeny(t *testing.T) {
	f := &fakeApprovalHTTP{toolName: "approval_prompt", verdict: `{"behavior":"deny","message":"policy forbids writes"}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	confirmer, stop, err := startHTTPConfirmer(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL), "")
	if err != nil {
		t.Fatalf("startHTTPConfirmer: %v", err)
	}
	defer stop()

	d := confirmer.Confirm(context.Background(), "Write", "worker w-7: write app.txt")
	if d.Allow {
		t.Fatalf("remote deny opened the gate: %+v", d)
	}
	if d.Reason != "policy forbids writes" {
		t.Errorf("deny reason = %q, want the orchestrator's message", d.Reason)
	}
}

// TestApprovalHTTPCustomTool: a foreign endpoint names its permission tool
// whatever it likes, and the descriptor's tool field points terva at it.
func TestApprovalHTTPCustomTool(t *testing.T) {
	f := &fakeApprovalHTTP{toolName: "gate_check", verdict: `{"behavior":"allow"}`}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	spec := fmt.Sprintf(`{"url":%q,"tool":"gate_check"}`, srv.URL)
	confirmer, stop, err := startHTTPConfirmer(context.Background(), spec, "")
	if err != nil {
		t.Fatalf("startHTTPConfirmer: %v", err)
	}
	defer stop()

	if d := confirmer.Confirm(context.Background(), "Bash", "worker w-1: rm tmp"); !d.Allow {
		t.Fatalf("custom-tool allow did not open the gate: %+v", d)
	}
	if calledAs, _, _ := f.snapshot(); calledAs != "gate_check" {
		t.Errorf("endpoint tool = %q, want the descriptor's gate_check", calledAs)
	}
}

// TestApprovalHTTPBearer: bearer_env rides as Authorization on the confirmation
// call, so a token-gated orchestrator is reachable without inlining the secret.
func TestApprovalHTTPBearer(t *testing.T) {
	f := &fakeApprovalHTTP{toolName: "approval_prompt", verdict: `{"behavior":"allow"}`, wantAuth: "t0ken"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	t.Setenv("ORCH_APPROVAL_TOKEN", "t0ken")
	spec := fmt.Sprintf(`{"url":%q,"bearer_env":"ORCH_APPROVAL_TOKEN"}`, srv.URL)
	confirmer, stop, err := startHTTPConfirmer(context.Background(), spec, "")
	if err != nil {
		t.Fatalf("startHTTPConfirmer: %v", err)
	}
	defer stop()

	if d := confirmer.Confirm(context.Background(), "Write", "worker w-1: touch x"); !d.Allow {
		t.Fatalf("authed allow did not open the gate: %+v", d)
	}
	if _, _, sawAuth := f.snapshot(); sawAuth != "Bearer t0ken" {
		t.Errorf("Authorization on the confirmation call = %q, want Bearer t0ken", sawAuth)
	}
}

// TestApprovalHTTPUnreachable: an endpoint that never answers fails the carrier's
// startup, so the caller keeps the gate's refuse-by-default — losing contact with
// the orchestrator must never open the gate.
func TestApprovalHTTPUnreachable(t *testing.T) {
	// A port nothing listens on: the initialize handshake fails, so Start fails.
	confirmer, stop, err := startHTTPConfirmer(context.Background(), `{"url":"http://127.0.0.1:1/mcp"}`, "")
	if err == nil {
		if stop != nil {
			stop()
		}
		t.Fatalf("an unreachable endpoint must fail startup, got confirmer %v", confirmer)
	}
}

// TestApprovalHTTPFailsClosedOnError: the handshake succeeds but the confirmation
// call errors (HTTP 500) — the confirmer must deny, not open. The safety-critical
// direction: an endpoint that breaks mid-run cannot become an allow.
func TestApprovalHTTPFailsClosedOnError(t *testing.T) {
	// A handler that completes initialize/tools/list but 500s the tools/call.
	var mu sync.Mutex
	handshakeDone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method == "tools/call" {
			http.Error(w, "orchestrator boom", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		handshakeDone = true
		mu.Unlock()
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s")
		}
		result := `{}`
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"o","version":"1"}}`
		case "tools/list":
			result = `{"tools":[{"name":"approval_prompt","inputSchema":{"type":"object"}}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, result)
	}))
	defer srv.Close()

	confirmer, stop, err := startHTTPConfirmer(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL), "")
	if err != nil {
		t.Fatalf("startHTTPConfirmer (handshake should succeed): %v", err)
	}
	defer stop()
	mu.Lock()
	ok := handshakeDone
	mu.Unlock()
	if !ok {
		t.Fatal("handshake never completed; test would not exercise the mid-run failure")
	}

	d := confirmer.Confirm(context.Background(), "Write", "worker w-1: write secret")
	if d.Allow {
		t.Fatalf("a 500 on the confirmation call opened the gate: %+v", d)
	}
}
