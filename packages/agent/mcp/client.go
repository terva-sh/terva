// Package mcp is a minimal Model Context Protocol client: tools only, over two
// transports behind one seam (see transport.go). A stdio server is a subprocess
// terva spawns — the same out-of-process posture as extensions and connectors
// (speak newline-delimited JSON, never link code in). An http server is a remote
// Streamable-HTTP endpoint terva POSTs to (build-tagged, default on; see
// mcp_http.go).
//
// On SSE: the DEPRECATED transport is the 2024-era standalone HTTP+SSE (two
// endpoints), which we never implement. Its replacement, Streamable HTTP (single
// endpoint, spec 2025-03-26 — a POST response that may upgrade to an SSE stream),
// is the live remote-MCP wire and is exactly what the http transport speaks.
//
// Deliberately not implemented (yet): resources, prompts, sampling, elicitation,
// and the standalone GET SSE channel for server-initiated messages. Tools are the
// value; the rest can arrive behind the same seam when wanted.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// protocolVersion is what we offer at initialize. Servers negotiate
// down; whatever they answer is accepted and recorded (tools/list and
// tools/call have been wire-stable across versions).
const protocolVersion = "2025-06-18"

// ServerConfig configures one MCP server. It describes a stdio subprocess by
// default; setting Transport to "http" describes a remote Streamable-HTTP
// endpoint instead. The two shapes are mutually exclusive (validate rejects a
// config that mixes command with url).
type ServerConfig struct {
	// --- stdio (the default transport) ---
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env adds variables to the (procenv-sanitized) child env.
	// Loader-injection keys and PATH overrides are rejected at load
	// time — config must not be able to re-introduce what procenv
	// strips.
	Env map[string]string `json:"env,omitempty"`

	// --- http (Streamable HTTP; requires the terva_no_mcp_http tag to be off) ---

	// Transport selects the wire: "" or "stdio" (default), or "http". An
	// unrecognised value fails cleanly at Start rather than silently falling back.
	Transport string `json:"transport,omitempty"`
	// URL is the Streamable-HTTP endpoint; required when Transport is "http".
	URL string `json:"url,omitempty"`
	// Headers are sent on every request. Values may reference ${ENV} — a token
	// is NEVER inlined here (see Auth), so a shared config can't ship a secret.
	Headers map[string]string `json:"headers,omitempty"`
	// Auth carries auth sugar. BearerEnv names an env var whose value is sent as
	// `Authorization: Bearer <value>` — the provider-key posture, so the token
	// lives in the environment, not the config file.
	Auth struct {
		BearerEnv string `json:"bearer_env,omitempty"`
	} `json:"auth,omitempty"`

	// TimeoutMS bounds each tools/call (default 60s).
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// Config is the JSON shape under the user config's "mcp" key.
type Config struct {
	Servers map[string]ServerConfig `json:"servers,omitempty"`
}

// ToolDef is one tool as reported by tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Annotations carries the spec's behavior hints; readOnlyHint
	// feeds terva's permission classification (plan-mode admission).
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// jsonrpc plumbing ----------------------------------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"` // nil for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	// Method is set on server-initiated requests/notifications, which
	// we acknowledge-or-ignore (tools-only client).
	Method string `json:"method"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// Client is one running MCP server connection. Safe for concurrent CallTool
// use: id allocation and sends are serialized, responses correlate by id. It is
// transport-agnostic — the wire (a stdio subprocess, a Streamable-HTTP session)
// lives behind the transport seam.
type Client struct {
	name      string
	transport transport
	timeout   time.Duration

	idMu   sync.Mutex // guards nextID allocation
	nextID int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	// done is closed when the transport's Incoming channel closes (the server
	// died or was stopped and the dispatch loop has exited). The Manager watches
	// this to withdraw a server that dies unexpectedly.
	done chan struct{}

	tools      []ToolDef
	serverInfo string
}

// Start spawns the server, performs the initialize handshake, and
// lists its tools. The context bounds startup only. cwd, when non-empty,
// is the working directory the server subprocess runs in — the user's
// project directory — so relative-path and directory-listing MCP tools
// resolve against the project rather than terva's launch directory (which
// under a carrier/daemon or an explicit --cwd is a different place).
func Start(ctx context.Context, name string, cfg ServerConfig, cwd string, stderr io.Writer) (*Client, error) {
	timeout := 60 * time.Second
	if cfg.TimeoutMS > 0 {
		timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	tr, err := newTransport(cfg, cwd, stderr)
	if err != nil {
		return nil, err
	}
	c := &Client{
		name:      name,
		transport: tr,
		timeout:   timeout,
		pending:   map[int64]chan rpcResponse{},
		done:      make(chan struct{}),
	}
	go c.dispatchLoop()

	if err := c.initialize(ctx); err != nil {
		c.Stop()
		return nil, err
	}
	if err := c.listTools(ctx); err != nil {
		c.Stop()
		return nil, err
	}
	return c, nil
}

// Name returns the configured server name.
func (c *Client) Name() string { return c.name }

// Tools returns the defs captured at startup.
func (c *Client) Tools() []ToolDef { return c.tools }

// Done returns a channel closed when the server's stdout has closed —
// the subprocess died or was stopped and readLoop has exited. The
// Manager watches this to withdraw a server that dies unexpectedly.
func (c *Client) Done() <-chan struct{} { return c.done }

// dispatchLoop reads inbound frames from the transport and correlates each to
// the caller waiting on its id. It is transport-agnostic: the transport funnels
// every frame here regardless of wire. Parsing lives here, not in the transport,
// so a stdio subprocess and an HTTP session share one correlation path.
func (c *Client) dispatchLoop() {
	for frame := range c.transport.Incoming() {
		var resp rpcResponse
		if err := json.Unmarshal(frame, &resp); err != nil {
			continue // not ours to fix; skip the frame
		}
		// Server-initiated traffic: a tools-only client answers nothing and
		// ignores notifications. (Requests would need an error reply to be
		// spec-pristine; in practice tools-only servers don't send any.)
		if resp.Method != "" && resp.Result == nil && resp.Error == nil {
			continue
		}
		if resp.ID == nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[*resp.ID]
		delete(c.pending, *resp.ID)
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	// Incoming closed: the server died or the session ended. Fail everything
	// pending so callers don't hang until their timeout.
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResponse{Error: &rpcError{Code: -1, Message: "mcp server closed the connection"}}
	}
	c.mu.Unlock()
	// Signal death exactly once (dispatchLoop runs once per client). The
	// Manager's watcher wakes on this to decide whether the exit was a crash to
	// withdraw or an intentional Stop.
	close(c.done)
}

// call sends one request and waits for its response or timeout.
func (c *Client) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("mcp server is not running")
	}
	c.mu.Unlock()

	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	c.idMu.Unlock()

	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(ctx, rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s timed out after %s", method, timeout)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *Client) notify(method string, params any) error {
	// A notification is fire-and-forget and has no id to time out on, so it uses
	// a background context.
	return c.write(context.Background(), rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// write marshals a JSON-RPC message and hands it to the transport, which owns
// wire framing (a newline for stdio, a POST body for http).
func (c *Client) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.transport.Send(ctx, b)
}

func (c *Client) initialize(ctx context.Context) error {
	res, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "terva", "version": "1"},
	}, 15*time.Second)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(res, &init)
	c.serverInfo = init.ServerInfo.Name
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *Client) listTools(ctx context.Context) error {
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.call(ctx, "tools/list", params, 15*time.Second)
		if err != nil {
			return fmt.Errorf("tools/list: %w", err)
		}
		var page struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return fmt.Errorf("tools/list decode: %w", err)
		}
		c.tools = append(c.tools, page.Tools...)
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// ContentItem is one block of a tool result.
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`     // base64, type=image
	MimeType string `json:"mimeType,omitempty"` // type=image
}

// CallResult is the outcome of tools/call.
type CallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// CallTool invokes one tool with raw JSON arguments.
func (c *Client) CallTool(ctx context.Context, tool string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := c.call(ctx, "tools/call", map[string]any{
		"name":      tool,
		"arguments": args,
	}, c.timeout)
	if err != nil {
		return CallResult{}, err
	}
	var out CallResult
	if err := json.Unmarshal(res, &out); err != nil {
		return CallResult{}, fmt.Errorf("tools/call decode: %w", err)
	}
	return out, nil
}

// Stop marks the client closed and tears the transport down (the polite MCP
// shutdown: stdin close + reap for stdio, session DELETE for http). When the
// server has already died, Close returns promptly.
func (c *Client) Stop() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.transport.Close()
}
