package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/provider/lineframe"
)

// Team answers a Talkoot member's calls to its seat tools, which the worker
// makes through `terva mcp-talkoot-bridge` (decision 0023). The host binds it
// to one worker, so no call names a member: the host finds the member that
// holds the worker's seat on each call.
type Team func(ctx context.Context, tool string, input json.RawMessage) mcpbridge.TeamReply

// WithTeam gives the runner the host's Team for this worker. A backend with
// TeamBridge then serves the seat tools on a socket of its own. It returns r.
func (r *Runner) WithTeam(t Team) *Runner {
	r.team = t
	return r
}

// teamSocketPath derives a worker's Talkoot socket from its inbox socket, as
// approvalSocketPath does: same directory, ".tk" in place of ".sock", so it is
// no longer than the inbox path and unique per agent.
func teamSocketPath(inbox string) string {
	return strings.TrimSuffix(inbox, ".sock") + ".tk"
}

// serveTeam opens the team socket at path and serves it until Close. ctx is
// the run's context.
//
// ⚠️ The 0600 permissions keep other users out, not other processes of this
// user. Any of those can dial the socket and speak as the member, as they can
// dial the approval socket. Decision 0023 copies the approval socket's
// boundary, and docs/permissions.md gives the same custody gap for a member
// that runs bash.
func (r *Runner) serveTeam(ctx context.Context, path string) (*socketListener, error) {
	return serveSocket(path, func(conn net.Conn) { r.handleTeamConn(ctx, conn) })
}

// handleTeamConn answers one seat-tool call. A malformed request gets an error
// reply rather than a dropped connection, so the model reads what went wrong.
func (r *Runner) handleTeamConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	// REJECT, as handleApprovalConn does: the connection carries one call.
	line, tooLong, err := lineframe.ReadFrame(bufio.NewReader(conn), lineframe.DefaultMaxBytes)
	if tooLong {
		writeTeamReply(conn, mcpbridge.TeamReply{Text: "the tool call exceeded the frame limit", IsError: true})
		return
	}
	if len(bytes.TrimSpace(line)) == 0 && err != nil {
		return // the bridge hung up before it called
	}
	var req mcpbridge.TeamRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		writeTeamReply(conn, mcpbridge.TeamReply{Text: "malformed tool call", IsError: true})
		return
	}
	writeTeamReply(conn, r.team(hangupContext(ctx, conn), req.Tool, req.Input))
}

// hangupContext is ctx, cancelled when the bridge closes conn. The bridge sends
// one call and then only reads, so the read ends when it hangs up, because the
// worker stopped. A question that waits for a person then closes its card
// rather than wait for an answer no one reads. Stray bytes do not end the
// call: only a failed read, which a close or a reset gives.
func hangupContext(ctx context.Context, conn net.Conn) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		buf := make([]byte, 64)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()
	return ctx
}

func writeTeamReply(conn net.Conn, reply mcpbridge.TeamReply) {
	b, _ := json.Marshal(reply)
	_, _ = conn.Write(append(b, '\n'))
}

// teamBridgeLoaded reads an agent_ready event for the team server's status. A
// backend with TeamBridge reports its MCP servers there as mcp_servers, a map
// from server name to status. Only "connected" counts: a member without its
// seat tools cannot answer its team, so the runner stops it rather than let it
// work unheard.
func teamBridgeLoaded(data map[string]any) error {
	servers, ok := data["mcp_servers"].(map[string]any)
	if !ok {
		return fmt.Errorf("the worker did not report its MCP servers, so the Talkoot bridge %s is not known to have loaded", mcpbridge.TeamServerName)
	}
	status, _ := servers[mcpbridge.TeamServerName].(string)
	if status == "connected" {
		return nil
	}
	if status == "" {
		status = "absent"
	}
	return fmt.Errorf("the Talkoot bridge %s did not load in the worker (status %s)", mcpbridge.TeamServerName, status)
}
