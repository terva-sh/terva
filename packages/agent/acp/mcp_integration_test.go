//go:build terva_acp

package acp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// The mcpstub lives in the mcp package's testdata; build it once per test
// binary, mirroring mcp/mcp_test.go's buildStub. From the acp package dir the
// source is at ../mcp/testdata/cmd/mcpstub.
var (
	acpStubOnce sync.Once
	acpStubPath string
	acpStubErr  error
)

func buildMCPStub(t *testing.T) string {
	t.Helper()
	acpStubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "acp-mcpstub")
		if err != nil {
			acpStubErr = err
			return
		}
		out := filepath.Join(dir, "mcpstub")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		// Resolve the stub source relative to this package's directory so the
		// test works regardless of the process cwd.
		_, thisFile, _, _ := runtime.Caller(0)
		src := filepath.Join(filepath.Dir(thisFile), "..", "mcp", "testdata", "cmd", "mcpstub")
		if b, berr := exec.Command("go", "build", "-o", out, src).CombinedOutput(); berr != nil {
			acpStubErr = berr
			_ = b
			return
		}
		acpStubPath = out
	})
	if acpStubErr != nil {
		t.Fatalf("build mcpstub: %v", acpStubErr)
	}
	return acpStubPath
}

// TestACPSessionNewWiresMCPTool is the Phase 4a integration test: drive
// session/new with a stdio mcpServers entry pointing at the mcpstub, then
// prove (1) the namespaced MCP tool (mcp_<server>_<tool>) is registered on the
// session's agent registry, and (2) a fake provider that calls that tool gets
// a real round-tripped result (the stub's `add` returns the sum), narrated as
// a tool_call + tool_call_update on the session/update stream. This exercises
// the whole seam: ParseMCPServers → mcp.Config → StartAll → MergeExtensionTools
// → the agent's confirm-gate ladder → Call → result.
func TestACPSessionNewWiresMCPTool(t *testing.T) {
	stub := buildMCPStub(t)

	// The fake provider calls mcp_calc_add{a:2,b:40} on turn 1, then finishes.
	client := &multiToolClient{
		toolName:  NamespaceMCP("calc", "add"),
		toolArgs:  `{"a":2,"b":40}`,
		toolTurns: 1,
	}
	factory := &fakeFactory{
		client:     client,
		tools:      core.Registry{}, // no base tools; only the MCP tools should appear
		mcpEnabled: true,
	}

	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})

	// session/new carrying one stdio MCP server (the stub), named "calc".
	newRes := h.call(MethodSessionNew, map[string]any{
		"cwd": t.TempDir(),
		"mcpServers": []map[string]any{
			{"name": "calc", "command": stub, "args": []string{}},
		},
	})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}

	// (1) The namespaced MCP tool must be registered on the agent's registry.
	reg := factory.lastRegistry()
	want := NamespaceMCP("calc", "add")
	if reg == nil {
		t.Fatal("no registry captured; MCP path did not run")
	}
	if _, ok := reg[want]; !ok {
		t.Fatalf("registry is missing the namespaced MCP tool %q; have %v", want, registryNames(reg))
	}

	// (2) The fake provider calls it; the turn must complete with the stub's
	// real result round-tripped (add 2+40 = 42).
	promptRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "add them"}},
	})
	if sr, _ := promptRes["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("stopReason = %q; want %q", promptRes["stopReason"], StopEndTurn)
	}

	// The tool_call + tool_call_update for the MCP tool must appear, and the
	// completed update must carry the stub's "42" result text.
	var sawToolCall, sawResult42 bool
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd == nil {
			continue
		}
		switch upd["sessionUpdate"] {
		case UpdateToolCall:
			if upd["toolCallId"] == "call-1" {
				sawToolCall = true
			}
		case UpdateToolCallUpdate:
			if content, ok := upd["content"].([]any); ok {
				for _, ci := range content {
					cm, _ := ci.(map[string]any)
					inner, _ := cm["content"].(map[string]any)
					if text, _ := inner["text"].(string); text == "42" {
						sawResult42 = true
					}
				}
			}
		}
	}
	if !sawToolCall {
		t.Error("no tool_call for the MCP tool on the session/update stream")
	}
	if !sawResult42 {
		t.Error("MCP tool result (42) did not round-trip onto a tool_call_update")
	}

	cancel()
	_ = caW.Close()
	_ = acW.Close()
	_ = caR.Close()
	_ = acR.Close()
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
	}
}

func registryNames(reg core.Registry) []string {
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	return out
}

// NamespaceMCP mirrors mcp.NamespaceTool for the test's expected names without
// importing the sanitizer (the stub server + tool names are already clean):
// mcp_<server>_<tool>.
func NamespaceMCP(server, tool string) string { return "mcp_" + server + "_" + tool }
