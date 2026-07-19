package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/privfs"
)

// approvalSocketPath derives a worker's MCP-approval socket from its inbox
// socket: same directory, ".ap" in place of ".sock". Always SHORTER than the
// inbox path (which the swarm already fit under the unix-socket length cap), so
// it fits too; and unique per agent because whatever made the inbox path unique
// — a per-id directory, or the id embedded in the filename on the tmp-fallback
// path — is preserved.
func approvalSocketPath(inbox string) string {
	return strings.TrimSuffix(inbox, ".sock") + ".ap"
}

// approvalListener serves one worker's MCP approval socket. Each accepted
// connection is one approval question (from the `terva mcp-approval-bridge` the
// worker runs as its permission tool): read a Request, route it to the runner's
// Confirmer via decide, write back the Reply. It is the socket-carrier sibling
// of handleAsk's stdin carrier — both terminate at the same decide, so a
// worker's approval reaches the identical human card whichever backend asked.
type approvalListener struct {
	ln   net.Listener
	path string
}

// serveApprovals opens the approval socket at path (0600 — perms are the auth)
// and serves it until Close. ctx is the run's context: it cancels an approval
// parked on a human when the worker is stopped, so a teardown denies rather than
// hanging.
func (r *Runner) serveApprovals(ctx context.Context, path string) (*approvalListener, error) {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Best-effort cleanup of a stale socket from a prior run at this path.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Perms are the auth. net.Listen honours umask, which can leave the socket
	// group/world-reachable; tighten to 0600 explicitly so only this user can
	// dial it. (The same posture --web-addr unix: relies on.)
	_ = os.Chmod(path, 0o600)

	al := &approvalListener{ln: ln, path: path}
	go al.acceptLoop(ctx, r)
	return al, nil
}

func (al *approvalListener) acceptLoop(ctx context.Context, r *Runner) {
	for {
		conn, err := al.ln.Accept()
		if err != nil {
			return // listener closed (Close) — the run is tearing down
		}
		go r.handleApprovalConn(ctx, conn)
	}
}

// Close stops accepting and removes the socket file. Idempotent enough for a
// defer: a second Close just re-errors on the closed listener, which we ignore.
func (al *approvalListener) Close() error {
	err := al.ln.Close()
	_ = os.Remove(al.path)
	return err
}

// handleApprovalConn answers one approval question. On a malformed request it
// denies (fail closed) rather than dropping the connection, so the bridge gets a
// verdict to return instead of a hang.
func (r *Runner) handleApprovalConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if len(bytes.TrimSpace(line)) == 0 && err != nil {
		return // the bridge hung up before asking; nothing to answer
	}
	var req mcpbridge.Request
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		writeApprovalReply(conn, mcpbridge.Reply{Allow: false, Reason: "malformed approval request"})
		return
	}
	d := r.decide(ctx, req.Tool, req.Preview)
	writeApprovalReply(conn, mcpbridge.Reply{Allow: d.Allow, Reason: d.Reason})
}

func writeApprovalReply(conn net.Conn, reply mcpbridge.Reply) {
	b, _ := json.Marshal(reply)
	_, _ = conn.Write(append(b, '\n'))
}
