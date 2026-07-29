package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/core"
)

// mcpApprovalConfirmer routes a tool confirmation through terva's OWN MCP client
// to an approval tool, mapping its {behavior} result back to a
// core.ConfirmDecision. It is the MCP carrier of core.Confirmer, and it is
// transport-blind: the client underneath may be a local stdio bridge (`terva rpc
// --approval-socket`, the terva:portable worker) or a REMOTE Streamable-HTTP
// endpoint (`--approval-http`, a foreign orchestrator gating the worker over the
// network). Either way the contract is identical to what a Claude worker reaches
// via --permission-prompt-tool — args {tool_name, preview}, result {behavior} —
// so the same result shape gates a config-opaque terva worker, a foreign claude
// one, and a remote HTTP orchestrator. Only the transport under the client and
// the tool NAME differ (our bridge exposes mcpbridge.ToolName; a foreign endpoint
// names its own, carried in `tool`).
//
// Fail closed: any transport failure, an empty or unparseable result, or a
// non-"allow" behavior denies. Losing contact with the orchestrator must never
// open the gate.
type mcpApprovalConfirmer struct {
	ctx    context.Context
	client *mcp.Client
	tool   string // the permission-prompt tool to call on the endpoint
}

// withEitherDone returns a context cancelled when either parent is. The two
// lifetimes here are genuinely independent — the caller's is a turn's or a
// worker's, c.ctx is the rpc server's — so neither is an ancestor of the other
// and a select on one alone would miss the other's end. AfterFunc costs no
// goroutine and is released by the returned cancel.
func withEitherDone(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	stop := context.AfterFunc(b, cancel)
	return ctx, func() { stop(); cancel() }
}

// ctx is the calling turn's; c.ctx is the server's. The call is bounded by
// whichever ends first — an orchestrator that never answers no longer holds a
// cancelled turn open for the full approval timeout.
func (c *mcpApprovalConfirmer) Confirm(ctx context.Context, toolName, preview string) core.ConfirmDecision {
	// terva already has a rendered preview, so pass it directly — the endpoint
	// prefers an explicit preview over one derived from Claude-style input.
	args, _ := json.Marshal(map[string]any{"tool_name": toolName, "preview": preview})
	callCtx, cancel := withEitherDone(ctx, c.ctx)
	defer cancel()
	res, err := c.client.CallTool(callCtx, c.tool, args)
	if err != nil {
		return core.ConfirmDecision{Allow: false, Reason: "approval endpoint unreachable: " + err.Error()}
	}
	if len(res.Content) == 0 || res.Content[0].Text == "" {
		return core.ConfirmDecision{Allow: false, Reason: "empty approval result from the endpoint"}
	}
	var pr struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &pr); err != nil {
		return core.ConfirmDecision{Allow: false, Reason: "unparseable approval result from the endpoint"}
	}
	if pr.Behavior == "allow" {
		return core.ConfirmDecision{Allow: true}
	}
	reason := pr.Message
	if reason == "" {
		reason = "denied by the orchestrator"
	}
	return core.ConfirmDecision{Allow: false, Reason: reason}
}

// approvalHTTPTimeoutMS is the default per-call bound for both carriers: a human
// may take minutes to answer, and expiry denies (the designed "deny-and-park")
// rather than hanging the worker's turn forever.
const approvalHTTPTimeoutMS = 10 * 60 * 1000

// startBridgeConfirmer spawns the approval bridge as an MCP server via terva's
// own client and returns a Confirmer that routes through it, plus a stop func.
// A generous per-call timeout (the bridge blocks on a human) means a stalled
// approval eventually times out to deny — the designed "deny-and-park" — rather
// than hanging the worker's turn forever. Returns (nil, nil, err) if the bridge
// cannot start; the caller then leaves the gate's refuse-by-default in place.
func startBridgeConfirmer(ctx context.Context, exe, socket, cwd string) (core.Confirmer, func(), error) {
	client, err := mcp.Start(ctx, mcpbridge.ServerName, mcp.ServerConfig{
		Command:   exe,
		Args:      []string{"mcp-approval-bridge", "--socket", socket},
		TimeoutMS: approvalHTTPTimeoutMS,
	}, cwd, nil)
	if err != nil {
		return nil, nil, err
	}
	return &mcpApprovalConfirmer{ctx: ctx, client: client, tool: mcpbridge.ToolName}, client.Stop, nil
}

// approvalHTTPConfig is the descriptor carried by --approval-http: a foreign
// orchestrator gating this worker's tool calls through a REMOTE Streamable-HTTP
// MCP permission endpoint. It maps onto an http mcp.ServerConfig plus the
// endpoint's tool name. A token is never inlined — BearerEnv names an env var, so
// a composed worker command can be logged without leaking the secret (the same
// posture as mcp.ServerConfig.Auth).
type approvalHTTPConfig struct {
	URL       string            `json:"url"`
	Tool      string            `json:"tool,omitempty"`       // default mcpbridge.ToolName
	BearerEnv string            `json:"bearer_env,omitempty"` // env var -> Authorization: Bearer
	Headers   map[string]string `json:"headers,omitempty"`    // values may reference ${ENV}
	TimeoutMS int               `json:"timeout_ms,omitempty"` // default approvalHTTPTimeoutMS
}

// startHTTPConfirmer connects terva's MCP client to a remote HTTP approval
// endpoint described by spec (the --approval-http JSON) and returns a Confirmer
// that routes tool confirmations to it, plus a stop func. This is the payoff of
// the Streamable-HTTP transport: the confirmer is transport-blind, so a remote
// orchestrator speaking the same {behavior} permission-tool contract our local
// bridge serves can gate the worker over the network. Returns (nil, nil, err) if
// the spec is invalid or the endpoint cannot be reached — the caller then leaves
// the gate's refuse-by-default in place (fail closed). A build with the
// terva_no_mcp_http tag has no http transport, so Start fails "not compiled in"
// and the run stays refuse-by-default, never silently open.
func startHTTPConfirmer(ctx context.Context, spec, cwd string) (core.Confirmer, func(), error) {
	var cfg approvalHTTPConfig
	if err := json.Unmarshal([]byte(spec), &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse --approval-http: %w", err)
	}
	if cfg.URL == "" {
		return nil, nil, errors.New("--approval-http: url is required")
	}
	tool := cfg.Tool
	if tool == "" {
		tool = mcpbridge.ToolName
	}
	timeout := cfg.TimeoutMS
	if timeout == 0 {
		timeout = approvalHTTPTimeoutMS
	}
	sc := mcp.ServerConfig{
		Transport: "http",
		URL:       cfg.URL,
		Headers:   cfg.Headers,
		TimeoutMS: timeout,
	}
	sc.Auth.BearerEnv = cfg.BearerEnv
	client, err := mcp.Start(ctx, "approval_http", sc, cwd, nil)
	if err != nil {
		return nil, nil, err
	}
	return &mcpApprovalConfirmer{ctx: ctx, client: client, tool: tool}, client.Stop, nil
}
