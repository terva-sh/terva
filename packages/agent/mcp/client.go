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
//
// A server request still gets an answer. ping gets an empty result, and every
// other method gets JSON-RPC error -32601 (method not found). Notifications are
// ignored. A server that stops reading loses replies rather than stall the
// client (see takeReplySlot).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// protocolVersion is what we offer at initialize. Servers negotiate
// down; whatever they answer is accepted (tools/list and tools/call have
// been wire-stable across versions) and handed to the transport, which
// carries it where its wire needs it (see protocolVersionSetter).
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
	// Annotations carries the spec's behavior hints, which feed terva's
	// permission classification (plan-mode admission, the read-only
	// auto-allow). readOnly says how the two combine.
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
		// DestructiveHint defaults to true in the spec, which gives it meaning
		// only when readOnlyHint is false. An absent key decodes false here, so
		// only an explicit true counts, and that is the contradiction readOnly
		// refuses.
		DestructiveHint bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// readOnly reports whether the tool claims no side effects. A tool that also
// claims destructiveHint contradicts itself, so the stricter claim wins.
func (d ToolDef) readOnly() bool {
	return d.Annotations.ReadOnlyHint && !d.Annotations.DestructiveHint
}

// jsonrpc plumbing ----------------------------------------------------

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"` // nil for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage is one inbound frame before dispatchLoop sorts it: a response to
// one of our requests, or a request or notification from the server. ID stays
// raw because a server request may carry a string id, which the reply echoes.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcResponse is what a pending call receives.
type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

// rpcReply answers a server request.
type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// errMethodNotFound is the JSON-RPC code for a method the receiver lacks.
const errMethodNotFound = -32601

const (
	// replyTimeout bounds one reply write, the budget notify has.
	replyTimeout = 15 * time.Second
	// maxPendingReplies caps the reply writes in flight, and with them the
	// goroutines a flooding server can make terva hold (see answer).
	maxPendingReplies = 64
	// replySlotWait is how long the dispatch loop waits for a reply slot: far
	// longer than a burst takes to drain, short enough to pause only once.
	replySlotWait = time.Second
)

// hasID reports whether a frame carries an id. MCP forbids a null id, so a
// null counts as none.
func hasID(id json.RawMessage) bool {
	return len(id) > 0 && string(id) != "null"
}

// Client is one running MCP server connection. Safe for concurrent CallTool
// use: id allocation and sends are serialized, responses correlate by id. It is
// transport-agnostic — the wire (a stdio subprocess, a Streamable-HTTP session)
// lives behind the transport seam.
type Client struct {
	name      string
	transport transport
	// kind is the configured wire ("stdio" or "http"), kept for the permission
	// classification: a remote server's read-only tool still sends its
	// arguments off the machine.
	kind    string
	timeout time.Duration

	idMu   sync.Mutex // guards nextID allocation
	nextID int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	// done is closed when the transport's Incoming channel closes (the server
	// died or was stopped and the dispatch loop has exited). The Manager watches
	// this to withdraw a server that dies unexpectedly.
	done chan struct{}
	// replySlots holds one token per reply write in flight.
	replySlots chan struct{}
	// replyStuck is set when a wait for a reply slot ran out, and cleared by
	// the next reply write that completes. While it is set, answer drops.
	replyStuck atomic.Bool
	// stopping is closed by Stop, so a dispatch loop waiting for a reply
	// slot never outlasts the client.
	stopping chan struct{}
	stopOnce sync.Once

	tools      []ToolDef
	serverInfo string
	// remoteRelay is set when the server's initialize result carries the
	// remote mark (markedRemote). Like kind, it is read only after Start
	// returns.
	remoteRelay bool
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
	c := newClient(name, tr, timeout)
	// kind is read only after Start returns, so this write does not race the
	// dispatch loop newClient started.
	c.kind = cfg.transportKind()

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

// newClient wraps a connected transport and starts its dispatch loop.
func newClient(name string, tr transport, timeout time.Duration) *Client {
	c := &Client{
		name:       name,
		transport:  tr,
		timeout:    timeout,
		pending:    map[int64]chan rpcResponse{},
		done:       make(chan struct{}),
		replySlots: make(chan struct{}, maxPendingReplies),
		stopping:   make(chan struct{}),
	}
	go c.dispatchLoop()
	return c
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
		var msg rpcMessage
		if err := json.Unmarshal(frame, &msg); err != nil {
			continue // not ours to fix; skip the frame
		}
		// A method with no result or error is server-initiated traffic: a
		// request, or a notification when it has no id. A server numbers its
		// requests in its own id space, so one can carry the id of a pending
		// call. The method is what keeps it from resolving that call.
		if msg.Method != "" && msg.Result == nil && msg.Error == nil {
			c.answer(msg)
			continue
		}
		var id int64
		if !hasID(msg.ID) || json.Unmarshal(msg.ID, &id) != nil {
			continue // our requests carry integer ids; this answers none of them
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- rpcResponse{Result: msg.Result, Error: msg.Error}
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

// answer replies to one server request: an empty result for ping, and
// errMethodNotFound for any other method, because a tools-only client serves
// nothing else. A notification has no id and gets no reply.
//
// The write runs off the dispatch loop. A stdio server that stops reading stdin
// until terva reads its stdout would otherwise deadlock the loop on the pipe.
// The pipeio gate (stdio) or a separate POST (http) keeps it apart from the
// request writes.
func (c *Client) answer(msg rpcMessage) {
	if !hasID(msg.ID) {
		return
	}
	reply := rpcReply{JSONRPC: "2.0", ID: msg.ID}
	if msg.Method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &rpcError{Code: errMethodNotFound, Message: "Method not found"}
	}
	if !c.takeReplySlot() {
		return
	}
	go func() {
		defer func() { <-c.replySlots }()
		ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
		defer cancel()
		if c.write(ctx, reply) == nil {
			c.replyStuck.Store(false)
		}
	}()
}

// takeReplySlot reserves a slot for one reply write, or reports that the reply
// must be dropped.
//
// Two simpler rules each failed under test. Dropping whenever the slots were
// full lost replies in a burst from a server that was reading: the dispatch loop
// outruns the writes that the gate serializes, so the slots fill although no
// write blocks. Blocking until a slot freed deadlocked on a server that floods
// requests before it reads: the loop stopped draining stdout, the server blocked
// on stdout and never reached stdin, and replyTimeout then tore the connection
// down.
//
// So full slots get a bounded wait. A burst frees a slot well inside
// replySlotWait, and the reply goes out. A server that stopped reading frees
// none: the wait runs out once, replyStuck is set, and from then on replies are
// dropped at once, so the loop keeps draining stdout. The next reply write that
// completes clears the flag.
func (c *Client) takeReplySlot() bool {
	select {
	case c.replySlots <- struct{}{}:
		return true
	default:
	}
	if c.replyStuck.Load() {
		return false
	}
	wait := time.NewTimer(replySlotWait)
	defer wait.Stop()
	select {
	case c.replySlots <- struct{}{}:
		return true
	case <-wait.C:
		c.replyStuck.Store(true)
		return false
	case <-c.stopping:
		return false
	}
}

// call sends one request and waits for its response or timeout.
func (c *Client) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("%s timed out after %s: %w", method, timeout, context.DeadlineExceeded))
	defer cancel()
	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	c.idMu.Unlock()

	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("mcp server is not running")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(ctx, rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.write(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
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
	c.remoteRelay = markedRemote(res)
	// 🔑 The server's answer, not our offer: the two differ when the server
	// negotiated down. It is set before the initialized notification, because
	// that is the first request the spec says must carry it.
	if vs, ok := c.transport.(protocolVersionSetter); ok {
		vs.setProtocolVersion(init.ProtocolVersion)
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// remoteMetaKey is the _meta key by which a server says that each call it
// takes leaves the machine. terva-mcp-bridge sets it in its initialize result.
// The bridge keeps its own copy of the key, because it takes no dependency on
// this package. Its integration test runs the built bridge against this reader.
const remoteMetaKey = "terva.sh/remote"

// markedRemote reports whether an initialize result carries the remote mark,
// JSON true at remoteMetaKey under _meta. Both keys match exactly, as MCP keys
// do, and any other value is no mark. Only the bool is kept, so no server text
// from _meta reaches the prompt.
//
// 🔑 The mark needs no trust, because it can only tighten. It moves a stdio
// server's read-only tools from a local read to network-read, which asks more.
// A server that sets it falsely costs itself prompts, and a server that leaves
// it out keeps the stdio treatment it had. That is decision 0009's rule that
// an untrusted layer may only restrict.
func markedRemote(res json.RawMessage) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(res, &top) != nil {
		return false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(top["_meta"], &meta) != nil {
		return false
	}
	return string(bytes.TrimSpace(meta[remoteMetaKey])) == "true"
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
	Content           []ContentItem   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
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
	c.stopOnce.Do(func() { close(c.stopping) })
	c.transport.Close()
}
