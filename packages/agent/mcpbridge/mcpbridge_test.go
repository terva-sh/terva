package mcpbridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/testsupport"
)

// TestBridgeRelaysApprovalThroughRealMCPClient is the whole serving path proven
// against a REAL MCP client (terva's own packages/agent/mcp): terva's client
// spawns the bridge as a stdio MCP server, discovers its one tool, and calls it;
// the bridge relays the question over the unix socket to a stub orchestrator and
// returns the verdict in Claude Code's permission-tool result shape. Using the
// real client is the point — if the bridge's initialize/tools/list/tools/call
// were wrong, this handshake would fail rather than a hand-rolled stand-in
// papering over it. (Claude Code drives the same MCP wire; 2b-ii validates that
// half live.)
func TestBridgeRelaysApprovalThroughRealMCPClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bridgeBin := buildBridge(t)
	sock := filepath.Join(shortSocketDir(t), "orch.sock")
	stub := newStubOrchestrator(t, sock, func(mcpbridge.Request) mcpbridge.Reply {
		return mcpbridge.Reply{Allow: true}
	})
	defer stub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startBridge(t, ctx, bridgeBin, sock)
	defer client.Stop()

	// The server exposes exactly the approval tool.
	tools := client.Tools()
	if len(tools) != 1 || tools[0].Name != mcpbridge.ToolName {
		t.Fatalf("tools = %+v, want one %q", tools, mcpbridge.ToolName)
	}

	res, err := client.CallTool(ctx, mcpbridge.ToolName,
		json.RawMessage(`{"tool_name":"Bash","input":{"command":"ls -la"}}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// A decision is a successful tool call — a deny is a valid answer, not a tool
	// failure — so isError must stay false whichever way the verdict goes.
	if res.IsError {
		t.Errorf("permission tool reported isError for a decision: %+v", res)
	}
	pr := parsePermission(t, res)
	if pr["behavior"] != "allow" {
		t.Fatalf("behavior = %v, want allow", pr["behavior"])
	}
	// updatedInput echoes the original input verbatim — the bridge never mutates.
	ui, _ := pr["updatedInput"].(map[string]any)
	if ui == nil || ui["command"] != "ls -la" {
		t.Errorf("updatedInput = %v, want the original input echoed", pr["updatedInput"])
	}
	// The orchestrator saw the tool name and a preview built from the input.
	if got := stub.tool(); got != "Bash" {
		t.Errorf("orchestrator saw tool %q, want Bash", got)
	}
	if got := stub.preview(); !strings.Contains(got, "ls -la") {
		t.Errorf("preview %q lost the args", got)
	}
}

// TestBridgeDenyCarriesReason: a denial round-trips as behavior:"deny" with the
// orchestrator's reason as the model-readable message.
func TestBridgeDenyCarriesReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bridgeBin := buildBridge(t)
	sock := filepath.Join(shortSocketDir(t), "orch.sock")
	stub := newStubOrchestrator(t, sock, func(mcpbridge.Request) mcpbridge.Reply {
		return mcpbridge.Reply{Allow: false, Reason: "the human said no"}
	})
	defer stub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startBridge(t, ctx, bridgeBin, sock)
	defer client.Stop()

	res, err := client.CallTool(ctx, mcpbridge.ToolName,
		json.RawMessage(`{"tool_name":"Write","input":{"path":"/etc/hosts"}}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	pr := parsePermission(t, res)
	if pr["behavior"] != "deny" {
		t.Fatalf("behavior = %v, want deny", pr["behavior"])
	}
	if msg, _ := pr["message"].(string); !strings.Contains(msg, "the human said no") {
		t.Errorf("deny message = %q, want the orchestrator's reason", msg)
	}
}

// TestBridgeFailsClosedWhenOrchestratorGone is the load-bearing safety property:
// if the orchestrator is unreachable, the bridge DENIES rather than allowing. An
// approval carrier that opened up the moment it lost the human would be the worst
// failure in this design. The bridge only dials on tools/call, so startup (which
// never touches the socket) still succeeds — the failure is isolated to the call.
func TestBridgeFailsClosedWhenOrchestratorGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bridgeBin := buildBridge(t)
	// A socket path with NO listener behind it.
	sock := filepath.Join(shortSocketDir(t), "absent.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startBridge(t, ctx, bridgeBin, sock)
	defer client.Stop()

	res, err := client.CallTool(ctx, mcpbridge.ToolName,
		json.RawMessage(`{"tool_name":"Bash","input":{"command":"rm -rf /"}}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	pr := parsePermission(t, res)
	if pr["behavior"] != "deny" {
		t.Fatalf("behavior = %v, want deny (fail closed when the orchestrator is gone)", pr["behavior"])
	}
	if msg, _ := pr["message"].(string); !strings.Contains(msg, "unavailable") {
		t.Errorf("deny message = %q, want it to name the lost connection", msg)
	}
}

// ---- helpers ----

func startBridge(t *testing.T, ctx context.Context, bin, sock string) *mcp.Client {
	t.Helper()
	client, err := mcp.Start(ctx, mcpbridge.ServerName, mcp.ServerConfig{
		Command: bin,
		Args:    []string{"--socket", sock},
	}, "", os.Stderr)
	if err != nil {
		t.Fatalf("start bridge via mcp client: %v", err)
	}
	return client
}

func parsePermission(t *testing.T, res mcp.CallResult) map[string]any {
	t.Helper()
	if len(res.Content) == 0 || res.Content[0].Type != "text" {
		t.Fatalf("result has no text content: %+v", res)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &m); err != nil {
		t.Fatalf("permission result is not json: %q", res.Content[0].Text)
	}
	return m
}

func buildBridge(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "bridge")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/bridge")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v\n%s", err, b)
	}
	return out
}

// shortSocketDir returns a tmp dir short enough to hold a unix socket under the
// ~104-byte cap (the runner's default tmp is too long on macOS). Mirrors the
// swarm inbox tests' helper.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "terva-ap-")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// stubOrchestrator stands in for the runner's approval socket: it accepts one
// connection per approval question, records what it was asked, and answers with
// a caller-supplied verdict.
type stubOrchestrator struct {
	ln     net.Listener
	decide func(mcpbridge.Request) mcpbridge.Reply

	mu      sync.Mutex
	gotTool string
	gotPrev string
}

func newStubOrchestrator(t *testing.T, path string, decide func(mcpbridge.Request) mcpbridge.Reply) *stubOrchestrator {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("stub listen: %v", err)
	}
	s := &stubOrchestrator{ln: ln, decide: decide}
	go s.serve()
	return s
}

func (s *stubOrchestrator) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if len(bytes.TrimSpace(line)) == 0 && err != nil {
				return
			}
			var req mcpbridge.Request
			if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
				return
			}
			s.mu.Lock()
			s.gotTool, s.gotPrev = req.Tool, req.Preview
			s.mu.Unlock()
			reply, _ := json.Marshal(s.decide(req))
			_, _ = c.Write(append(reply, '\n'))
		}()
	}
}

func (s *stubOrchestrator) tool() string    { s.mu.Lock(); defer s.mu.Unlock(); return s.gotTool }
func (s *stubOrchestrator) preview() string { s.mu.Lock(); defer s.mu.Unlock(); return s.gotPrev }
func (s *stubOrchestrator) Close()          { _ = s.ln.Close() }
