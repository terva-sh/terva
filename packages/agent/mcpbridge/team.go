package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"
)

// TeamServerName is the MCP server key of the Talkoot team server. A worker's
// CLI names its tools mcp__terva_talkoot__<tool>.
const TeamServerName = "terva_talkoot"

// TeamCallTimeoutMS bounds one call to the team server in a worker's MCP
// client. A question waits for a person, so the bound is the approval
// carrier's ten minutes, and not a tool call's usual minute.
const TeamCallTimeoutMS = 10 * 60 * 1000

// TeamQuestionWait is how long the orchestrator waits for a person to answer a
// worker's question. It ends before TeamCallTimeoutMS, so the worker reads why
// its question ended.
//
// 🔑 A worker's MCP client that gives up does not close the bridge's socket, so
// the orchestrator would keep the card open and could record an answer no one
// reads. The shorter wait on the orchestrator's side closes the card first, and
// it holds whatever bound the worker's CLI applies.
const TeamQuestionWait = TeamCallTimeoutMS*time.Millisecond - 30*time.Second

// TeamTool is one seat tool the team server lists: the name, description, and
// input schema a native member reads for the same tool.
type TeamTool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// TeamRequest is one seat-tool call the team server relays to the orchestrator
// over the unix socket, one call per connection, answered with a TeamReply.
//
// 🔑 It names no member. The orchestrator knows which worker owns the socket,
// and it finds that worker's member on each call, so a model cannot speak as
// another member.
type TeamRequest struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
}

// TeamReply is the orchestrator's answer to one call: the text a native tool
// returns, or the error it returns, with IsError set.
type TeamReply struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// ServeTeam runs the Talkoot team server on in/out. It lists tools and relays
// each call to the orchestrator over the unix socket at socketPath. It returns
// when in reaches EOF or ctx is cancelled.
//
// The caller passes the tools, so this package stays free of the tool code. The
// orchestrator refuses a tool it does not serve, whatever this list says.
func ServeTeam(ctx context.Context, in io.Reader, out io.Writer, socketPath string, tools []TeamTool) error {
	s := &server{socket: socketPath, out: out, name: TeamServerName}
	served := map[string]bool{}
	for _, t := range tools {
		s.tools = append(s.tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema,
		})
		served[t.Name] = true
	}
	s.call = func(ctx context.Context, req jsonrpcReq) { s.handleTeamCall(ctx, req, served) }
	return s.run(ctx, in)
}

func (s *server) handleTeamCall(ctx context.Context, req jsonrpcReq, served map[string]bool) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.reply(req.ID, nil, &rpcError{Code: -32602, Message: "invalid tools/call params"})
		return
	}
	if !served[params.Name] {
		s.reply(req.ID, nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name})
		return
	}
	input := params.Arguments
	if len(bytes.TrimSpace(input)) == 0 || string(bytes.TrimSpace(input)) == "null" {
		input = json.RawMessage("{}")
	}
	var reply TeamReply
	if err := s.roundTrip(ctx, "talkoot", TeamRequest{Tool: params.Name, Input: input}, &reply); err != nil {
		// The call did not happen, and the model reads why. Nothing retries it,
		// because a send that reached the router before the reply was lost
		// would go twice.
		reply = TeamReply{Text: "the talkoot did not answer: " + err.Error(), IsError: true}
	}
	s.reply(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": reply.Text}},
		"isError": reply.IsError,
	}, nil)
}
