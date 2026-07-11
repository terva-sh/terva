// Package mcp is a minimal Model Context Protocol client: stdio
// transport, tools only. It exists to plug the MCP server ecosystem
// into terva's tool registry through the same out-of-process posture
// as extensions and connectors — spawn a subprocess, speak
// newline-delimited JSON, never link code in.
//
// Deliberately not implemented (yet): HTTP transports, resources,
// prompts, sampling, elicitation. Tools are the value; the rest can
// arrive behind the same seam when wanted. SSE is skipped for good —
// the ecosystem deprecated it (see docs/plans/harness-landscape-2026.md A1).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/lineframe"
)

// protocolVersion is what we offer at initialize. Servers negotiate
// down; whatever they answer is accepted and recorded (tools/list and
// tools/call have been wire-stable across versions).
const protocolVersion = "2025-06-18"

// ServerConfig configures one stdio MCP server.
type ServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Env adds variables to the (procenv-sanitized) child env.
	// Loader-injection keys and PATH overrides are rejected at load
	// time — config must not be able to re-introduce what procenv
	// strips.
	Env map[string]string `json:"env,omitempty"`
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

// Client is one running MCP server subprocess. Safe for concurrent
// CallTool use: writes are serialized, responses correlate by id.
type Client struct {
	name    string
	cfg     ServerConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	timeout time.Duration

	writeMu sync.Mutex
	nextID  int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	// done is closed when readLoop exits (the server's stdout closed:
	// it died or was stopped). The Manager watches this to withdraw a
	// server that dies unexpectedly.
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
	c := &Client{
		name:    name,
		cfg:     cfg,
		timeout: timeout,
		pending: map[int64]chan rpcResponse{},
		done:    make(chan struct{}),
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	env := procenv.Inherited()
	for k, v := range cfg.Env {
		// Validated at config load; belt-and-braces here because this
		// is the actual trust boundary.
		if procenv.Disallowed(k) || k == "PATH" {
			continue
		}
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if stderr != nil {
		cmd.Stderr = stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", cfg.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin

	go c.readLoop(stdout)

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

func (c *Client) readLoop(stdout io.Reader) {
	// MCP tool results can be large (a whole file in a text block); read
	// through lineframe at the shared 4 MiB ceiling so one oversized frame
	// from a buggy or hostile server drops just that response (its caller
	// times out) instead of tearing down the whole connection the way
	// bufio.Scanner's ErrTooLong would. This minimal client has no logger
	// seam, so the skip is silent.
	fr := lineframe.NewReader(stdout, lineframe.DefaultMaxBytes, nil)
	for {
		line, err := fr.Read()
		if err != nil {
			break // io.EOF or a read error: the server closed stdout
		}
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // not ours to fix; skip the frame
		}
		// Server-initiated traffic: a tools-only client answers
		// nothing and ignores notifications. (Requests would need an
		// error reply to be spec-pristine; in practice tools-only
		// servers don't send any.)
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
	// EOF: the server died or closed stdout. Fail everything pending
	// so callers don't hang until their timeout.
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResponse{Error: &rpcError{Code: -1, Message: "mcp server closed the connection"}}
	}
	c.mu.Unlock()
	// Signal death exactly once (readLoop runs once per client). The Manager's
	// watcher wakes on this to decide whether the exit was a crash to withdraw
	// or an intentional Stop.
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

	c.writeMu.Lock()
	c.nextID++
	id := c.nextID
	c.writeMu.Unlock()

	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
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
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(b)
	return err
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

// Stop closes stdin (the polite MCP shutdown) and kills the process
// after a short grace period.
func (c *Client) Stop() {
	c.mu.Lock()
	closed := c.closed
	c.closed = true
	c.mu.Unlock()
	_ = c.stdin.Close()
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	if closed {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = c.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}
