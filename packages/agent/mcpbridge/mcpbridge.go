// Package mcpbridge is terva's stdio MCP approval server — the out-of-process
// shim that lets a foreign worker ask the orchestrating terva for tool
// permission. A Claude Code worker points --permission-prompt-tool at it; a
// `terva rpc --portable` worker points --approval-tool at it. Both reach the
// SAME place a worker's rpc-native ask reaches: the orchestrator's core.Confirmer
// and the human's card.
//
// It is the serving side terva did not have. packages/agent/mcp is an MCP
// *client*; terva ships no MCP *server*. Rather than grow one in-core — which
// would reverse a standing decision that inbound / port-listening / auth weight
// stays out of the small static core (see docs/proposals/external-agent-workers.md,
// "Why a bridge and not an HTTP endpoint") — the bridge is a companion binary,
// the same move as the OAuth callback bridge.
//
// The shape:
//
//	worker's MCP client  --tools/call-->        bridge (stdio MCP server, this pkg)
//	bridge  --Request over the unix socket-->   orchestrating terva --> core.Confirmer --> human card
//	bridge  <--Reply (verdict)------------       orchestrating terva
//	worker's MCP client  <--permission result--  bridge
//
// The unix socket's 0600 permissions ARE the auth — the pattern `--web-addr
// unix:` already proved. The bridge never listens on a port and never rides the
// web listener (behind haproxy every request there looks like loopback+cleartext,
// so loopback-implies-trust is unsafe on exactly the listener one would reach for).
//
// Fail-closed is the load-bearing property: if the orchestrator is unreachable,
// the bridge returns DENY, never allow. An approval carrier that opened up when
// it lost contact with the human would be the worst failure this design has.
package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
)

const (
	// ServerName is the MCP server key a worker's CLI addresses the bridge by:
	// the fully-qualified tool ref is mcp__<ServerName>__<ToolName>. Claude Code
	// derives it from the server's key in --mcp-config; terva's --approval-tool
	// names it directly. Keep the two in step via PermissionToolRef.
	ServerName = "terva_approvals"
	// ToolName is the single tool the bridge exposes.
	ToolName = "approval_prompt"
	// PermissionToolRef is what --permission-prompt-tool (claude) and
	// --approval-tool (terva:portable) name to route permission checks here.
	PermissionToolRef = "mcp__" + ServerName + "__" + ToolName

	// protocolVersion is what the server reports at initialize. It mirrors the
	// client's (packages/agent/mcp) offered version; MCP clients negotiate down
	// and neither terva's client nor Claude's validates it strictly.
	protocolVersion = "2025-06-18"
)

// Request is one approval question the bridge relays to the orchestrator over
// the unix socket — one question per connection, a single newline-terminated
// JSON line, answered with a Reply on the same connection.
type Request struct {
	Tool    string          `json:"tool"`            // the tool the worker wants to run
	Preview string          `json:"preview"`         // a one-line summary of its arguments (for the card)
	Input   json.RawMessage `json:"input,omitempty"` // the raw tool input, echoed back verbatim on allow
}

// Reply is the orchestrator's verdict — the socket's response frame. Its fields
// mirror the meaningful half of core.ConfirmDecision. It deliberately does NOT
// carry the remember-* flags: an MCP permission tool is asked per call and has
// no session memory for the orchestrator to grant across calls.
type Reply struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// permissionResult is the JSON the permission tool returns inside its single
// text content block — Claude Code's --permission-prompt-tool contract, which
// terva's own --approval-tool reader also consumes (one shape, two callers, so
// the conformance parallel covers approvals too). On allow, updatedInput is the
// input to actually run (we never mutate it — the original, echoed back). On
// deny, message is the model-readable reason.
type permissionResult struct {
	Behavior     string          `json:"behavior"` // "allow" | "deny"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// Serve runs the stdio MCP server on in/out, relaying each approval call to the
// orchestrator over the unix socket at socketPath. It returns when in reaches
// EOF (the worker's CLI closed the bridge) or ctx is cancelled.
func Serve(ctx context.Context, in io.Reader, out io.Writer, socketPath string) error {
	s := &server{socket: socketPath, out: out}
	return s.run(ctx, in)
}

type server struct {
	socket  string
	out     io.Writer
	writeMu sync.Mutex
}

// jsonrpcReq is one inbound MCP message. ID is kept raw so a request's id is
// echoed back byte-for-byte (number or string) and its absence marks a
// notification, which gets no reply.
type jsonrpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *server) run(ctx context.Context, in io.Reader) error {
	br := bufio.NewReader(in)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			s.handle(ctx, line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (s *server) handle(ctx context.Context, line []byte) {
	var req jsonrpcReq
	if err := json.Unmarshal(line, &req); err != nil {
		return // not a frame we can answer; drop it (no id to correlate anyway)
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	switch req.Method {
	case "initialize":
		s.reply(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": "1"},
		}, nil)
	case "notifications/initialized":
		// A notification — no response. (The client sends it after initialize.)
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": []any{toolDef()}}, nil)
	case "tools/call":
		s.handleCall(ctx, req)
	case "ping":
		s.reply(req.ID, map[string]any{}, nil)
	default:
		if !isNotification {
			s.reply(req.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
}

// toolDef describes the single approval tool. The input schema names the fields
// Claude Code's permission-prompt-tool call carries (tool_name + input); terva's
// own --approval-tool reader constructs the same shape.
func toolDef() map[string]any {
	return map[string]any{
		"name":        ToolName,
		"description": "Ask the orchestrating terva to approve or deny a tool call. Returns {behavior:\"allow\"|\"deny\"}.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name": map[string]any{"type": "string", "description": "the tool being requested"},
				"input":     map[string]any{"type": "object", "description": "the tool's input arguments"},
			},
			"required": []any{"tool_name"},
		},
	}
}

func (s *server) handleCall(ctx context.Context, req jsonrpcReq) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.reply(req.ID, nil, &rpcError{Code: -32602, Message: "invalid tools/call params"})
		return
	}
	if params.Name != ToolName {
		s.reply(req.ID, nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name})
		return
	}
	var call struct {
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
		// Preview is an optional card summary the caller can supply directly.
		// Claude's permission-prompt-tool sends only {tool_name, input}, so it is
		// derived from input; a terva --approval-tool caller already has a rendered
		// preview and passes it here so the card keeps its detail.
		Preview string `json:"preview"`
	}
	_ = json.Unmarshal(params.Arguments, &call)

	pv := call.Preview
	if pv == "" {
		pv = preview(call.Input)
	}
	reply, err := s.ask(ctx, Request{
		Tool:    call.ToolName,
		Preview: pv,
		Input:   call.Input,
	})

	var pr permissionResult
	switch {
	case err != nil:
		// The orchestrator is unreachable. Fail CLOSED — deny — never allow when
		// we've lost contact with the human who was supposed to decide.
		pr = permissionResult{Behavior: "deny", Message: "terva approval unavailable: " + err.Error()}
	case reply.Allow:
		input := call.Input
		if len(bytes.TrimSpace(input)) == 0 {
			input = json.RawMessage("{}")
		}
		pr = permissionResult{Behavior: "allow", UpdatedInput: input}
	default:
		msg := reply.Reason
		if msg == "" {
			msg = "denied by terva"
		}
		pr = permissionResult{Behavior: "deny", Message: msg}
	}

	text, _ := json.Marshal(pr)
	// isError stays false: the tool SUCCEEDED at producing a decision. A deny is
	// a valid answer, not a tool failure — Claude's contract reads behavior, not
	// isError, to gate the call.
	s.reply(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(text)}},
		"isError": false,
	}, nil)
}

// ask relays one Request to the orchestrator and waits for its Reply. It does
// NOT impose a short read deadline: a pending human card can take minutes, and
// the correct behaviour is to wait, not to time out into a guess. It aborts only
// if the orchestrator drops the connection (EOF → error → deny) or the bridge is
// shutting down (ctx cancelled).
func (s *server) ask(ctx context.Context, req Request) (Reply, error) {
	conn, err := net.Dial("unix", s.socket)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()

	// Unblock a long wait if the bridge is torn down mid-question.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Reply{}, err
	}
	respLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if len(bytes.TrimSpace(respLine)) == 0 {
		if err != nil {
			return Reply{}, err
		}
		return Reply{}, errors.New("empty approval reply from orchestrator")
	}
	var reply Reply
	if err := json.Unmarshal(bytes.TrimSpace(respLine), &reply); err != nil {
		return Reply{}, fmt.Errorf("malformed approval reply: %w", err)
	}
	return reply, nil
}

// preview compacts a tool's input into a one-line card summary. The card shows
// the tool name separately (Confirm(tool, preview)), so this is the arguments
// alone. Empty input yields an empty preview.
func preview(input json.RawMessage) string {
	s := strings.TrimSpace(string(input))
	if s == "" || s == "{}" || s == "null" {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, input) == nil {
		s = buf.String()
	}
	const max = 240
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// ---- jsonrpc write helpers (single-line JSON, mutex-guarded) ----

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// reply writes one jsonrpc response. id is echoed verbatim (null when absent);
// exactly one of result / rpcErr is set.
func (s *server) reply(id json.RawMessage, result any, rpcErr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		resp["id"] = json.RawMessage(id)
	} else {
		resp["id"] = nil
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(b)
	_, _ = s.out.Write([]byte("\n"))
}
