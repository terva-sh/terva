package agent

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

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/testsupport"
)

// TestMCPApprovalConfirmerRoutesThroughBridge is the terva:portable carrier's
// heart: terva's ConfirmGate -> mcpApprovalConfirmer -> terva's own MCP client ->
// the real bridge -> the orchestrator's socket -> a verdict -> ConfirmDecision.
// It uses the REAL packages/agent/mcp client and the REAL bridge, so a break in
// either the tool call or the {behavior} contract fails here rather than in a
// live terva:portable run. The preview terva renders reaches the card verbatim
// (the bridge prefers an explicit preview over a Claude-style input-derived one).
func TestMCPApprovalConfirmerRoutesThroughBridge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	bridgeBin := buildApprovalBridge(t)

	t.Run("allow", func(t *testing.T) {
		sock := filepath.Join(shortSockDir(t), "orch.sock")
		stub := serveApprovalStub(t, sock, func(mcpbridge.Request) mcpbridge.Reply {
			return mcpbridge.Reply{Allow: true}
		})
		defer stub.close()

		confirmer, stop := startTestConfirmer(t, bridgeBin, sock)
		defer stop()

		d := confirmer.Confirm("bash", "rm -rf /tmp/x")
		if !d.Allow {
			t.Errorf("decision = %+v, want allow", d)
		}
		// terva's rendered preview reaches the orchestrator card verbatim.
		if got := stub.tool(); got != "bash" {
			t.Errorf("orchestrator saw tool %q, want bash", got)
		}
		if got := stub.preview(); got != "rm -rf /tmp/x" {
			t.Errorf("orchestrator saw preview %q, want the rendered preview passed through", got)
		}
	})

	t.Run("deny carries reason", func(t *testing.T) {
		sock := filepath.Join(shortSockDir(t), "orch.sock")
		stub := serveApprovalStub(t, sock, func(mcpbridge.Request) mcpbridge.Reply {
			return mcpbridge.Reply{Allow: false, Reason: "the human said no"}
		})
		defer stub.close()

		confirmer, stop := startTestConfirmer(t, bridgeBin, sock)
		defer stop()

		d := confirmer.Confirm("write", "/etc/hosts")
		if d.Allow {
			t.Errorf("decision = %+v, want deny", d)
		}
		if !strings.Contains(d.Reason, "the human said no") {
			t.Errorf("deny reason = %q, want the orchestrator's reason", d.Reason)
		}
	})

	t.Run("fails closed when the orchestrator is gone", func(t *testing.T) {
		// A socket path with no listener: the bridge starts fine (it only dials on
		// a call) and the call denies.
		sock := filepath.Join(shortSockDir(t), "absent.sock")
		confirmer, stop := startTestConfirmer(t, bridgeBin, sock)
		defer stop()

		d := confirmer.Confirm("bash", "rm -rf /")
		if d.Allow {
			t.Errorf("a lost orchestrator must deny, got %+v", d)
		}
	})
}

// TestStartHTTPConfirmerRejectsBadDescriptor pins the --approval-http descriptor
// validation, which runs before any network I/O (so it holds regardless of the
// http-transport build tag): malformed JSON and a missing url both fail, and a
// failure leaves the caller to keep the gate's refuse-by-default (fail closed).
func TestStartHTTPConfirmerRejectsBadDescriptor(t *testing.T) {
	cases := []struct {
		name, spec, wantErr string
	}{
		{"malformed json", `{not json`, "parse --approval-http"},
		{"missing url", `{"tool":"approval_prompt"}`, "url is required"},
		{"empty url", `{"url":""}`, "url is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			confirmer, stop, err := startHTTPConfirmer(context.Background(), c.spec, "")
			if err == nil {
				if stop != nil {
					stop()
				}
				t.Fatalf("%s: want an error, got confirmer %v", c.name, confirmer)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: error = %q, want it to mention %q", c.name, err, c.wantErr)
			}
		})
	}
}

func startTestConfirmer(t *testing.T, bridgeBin, sock string) (*mcpApprovalConfirmer, func()) {
	t.Helper()
	ctx := context.Background()
	client, err := mcp.Start(ctx, mcpbridge.ServerName, mcp.ServerConfig{
		Command: bridgeBin,
		Args:    []string{"--socket", sock},
	}, "", os.Stderr)
	if err != nil {
		t.Fatalf("start bridge via mcp client: %v", err)
	}
	return &mcpApprovalConfirmer{ctx: ctx, client: client, tool: mcpbridge.ToolName}, client.Stop
}

func buildApprovalBridge(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "bridge")
	cmd := exec.Command("go", "build", "-o", out, "terva.sh/terva/packages/agent/mcpbridge/testdata/cmd/bridge")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v\n%s", err, b)
	}
	return out
}

func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "terva-rpcap-")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type approvalStub struct {
	ln     net.Listener
	decide func(mcpbridge.Request) mcpbridge.Reply

	mu      sync.Mutex
	gotTool string
	gotPrev string
}

func serveApprovalStub(t *testing.T, path string, decide func(mcpbridge.Request) mcpbridge.Reply) *approvalStub {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("stub listen: %v", err)
	}
	s := &approvalStub{ln: ln, decide: decide}
	go func() {
		for {
			c, err := ln.Accept()
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
				if json.Unmarshal(bytes.TrimSpace(line), &req) != nil {
					return
				}
				s.mu.Lock()
				s.gotTool, s.gotPrev = req.Tool, req.Preview
				s.mu.Unlock()
				reply, _ := json.Marshal(s.decide(req))
				_, _ = c.Write(append(reply, '\n'))
			}()
		}
	}()
	return s
}

func (s *approvalStub) tool() string    { s.mu.Lock(); defer s.mu.Unlock(); return s.gotTool }
func (s *approvalStub) preview() string { s.mu.Lock(); defer s.mu.Unlock(); return s.gotPrev }
func (s *approvalStub) close()          { _ = s.ln.Close() }
