package mcpbridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/mcpbridge"
)

// TestTeamServerRelaysACallThroughRealMCPClient proves the team server against
// terva's own MCP client: it lists the tools it was given, and a call reaches
// the orchestrator as one TeamRequest whose reply comes back as the result.
func TestTeamServerRelaysACallThroughRealMCPClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bin := buildBridge(t)
	sock := filepath.Join(shortSocketDir(t), "team.sock")
	stub := newStubTeam(t, sock, func(r mcpbridge.TeamRequest) mcpbridge.TeamReply {
		if r.Tool == "talkoot_roster" {
			return mcpbridge.TeamReply{Text: "the router is closed", IsError: true}
		}
		return mcpbridge.TeamReply{Text: "Sent message env-1 to lead."}
	})
	defer stub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startTeamBridge(t, ctx, bin, sock)
	defer client.Stop()

	var names []string
	for _, tl := range client.Tools() {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "talkoot_send,talkoot_roster" {
		t.Fatalf("tools = %v, want the two it was given", names)
	}

	res, err := client.CallTool(ctx, "talkoot_send", json.RawMessage(`{"body":"hi"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError || len(res.Content) == 0 || res.Content[0].Text != "Sent message env-1 to lead." {
		t.Fatalf("result = %+v, want the orchestrator's text", res)
	}
	got := stub.last()
	if got.Tool != "talkoot_send" || !strings.Contains(string(got.Input), `"body":"hi"`) {
		t.Errorf("orchestrator saw %+v, want the tool and its input", got)
	}

	// An orchestrator error reaches the model as a tool error, not a
	// protocol failure, so the model reads why.
	res, err = client.CallTool(ctx, "talkoot_roster", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || res.Content[0].Text != "the router is closed" {
		t.Errorf("result = %+v, want the error text with isError", res)
	}
	if in := string(stub.last().Input); in != "{}" {
		t.Errorf("a call with no arguments relayed input %q, want {}", in)
	}
}

// TestTeamServerRefusesAToolItDoesNotList pins that a call names one of the
// listed tools. The orchestrator never sees another.
func TestTeamServerRefusesAToolItDoesNotList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bin := buildBridge(t)
	sock := filepath.Join(shortSocketDir(t), "team.sock")
	stub := newStubTeam(t, sock, func(mcpbridge.TeamRequest) mcpbridge.TeamReply {
		return mcpbridge.TeamReply{Text: "ran"}
	})
	defer stub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startTeamBridge(t, ctx, bin, sock)
	defer client.Stop()

	if _, err := client.CallTool(ctx, "talkoot_propose", json.RawMessage(`{"why":"x"}`)); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("call of an unlisted tool: err = %v, want unknown tool", err)
	}
	if got := stub.last(); got.Tool != "" {
		t.Errorf("the orchestrator saw %q, want no call", got.Tool)
	}
}

// TestTeamServerReportsALostOrchestrator pins the failure a model reads when
// the socket has no listener: a tool error that says the talkoot did not
// answer, never a success.
func TestTeamServerReportsALostOrchestrator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bin := buildBridge(t)
	sock := filepath.Join(shortSocketDir(t), "absent.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := startTeamBridge(t, ctx, bin, sock)
	defer client.Stop()

	res, err := client.CallTool(ctx, "talkoot_send", json.RawMessage(`{"body":"hi"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "did not answer") {
		t.Errorf("result = %+v, want a tool error that names the lost talkoot", res)
	}
}

func startTeamBridge(t *testing.T, ctx context.Context, bin, sock string) *mcp.Client {
	t.Helper()
	client, err := mcp.Start(ctx, mcpbridge.TeamServerName, mcp.ServerConfig{
		Command: bin,
		Args:    []string{"--team", "--socket", sock},
	}, "", os.Stderr)
	if err != nil {
		t.Fatalf("start team bridge via mcp client: %v", err)
	}
	return client
}

// stubTeam stands in for the runner's team socket: one call per connection,
// answered by a caller-supplied function.
type stubTeam struct {
	ln     net.Listener
	answer func(mcpbridge.TeamRequest) mcpbridge.TeamReply

	mu  sync.Mutex
	got mcpbridge.TeamRequest
}

func newStubTeam(t *testing.T, path string, answer func(mcpbridge.TeamRequest) mcpbridge.TeamReply) *stubTeam {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("stub listen: %v", err)
	}
	s := &stubTeam{ln: ln, answer: answer}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req mcpbridge.TeamRequest
			if json.Unmarshal(bytes.TrimSpace(line), &req) == nil {
				s.mu.Lock()
				s.got = req
				s.mu.Unlock()
				reply, _ := json.Marshal(s.answer(req))
				_, _ = c.Write(append(reply, '\n'))
			}
			_ = c.Close()
		}
	}()
	return s
}

func (s *stubTeam) last() mcpbridge.TeamRequest { s.mu.Lock(); defer s.mu.Unlock(); return s.got }
func (s *stubTeam) Close()                      { _ = s.ln.Close() }
