package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/testsupport"
)

// TestRealTervaClientDrivesBridge is the done-criterion, proven with the REAL
// parts: terva's own mcp.Client spawns the built terva-mcp-bridge binary as a
// plain stdio `command` server, and reaches an OAuth-gated remote through it —
// discover, register, browser-consent, token, refresh all handled in the bridge,
// invisible to core. It closes the whole chain terva → (stdio) → bridge →
// (OAuth + Streamable-HTTP) → remote server.
//
// This test-only import of packages/agent/mcp does NOT couple the bridge binary
// to core (the production build excludes _test.go); it just uses the real client
// to prove interop. Note the bridge works even where core has no HTTP transport:
// terva spawns it over stdio and the bridge owns the HTTP + OAuth upstream.
func TestRealTervaClientDrivesBridge(t *testing.T) {
	bridgeBin := buildBridge(t)
	f := newFakeOAuthMCP(t)
	tokenDir := testsupport.TempDir(t)

	// One-time login populates the token store the relay will reuse.
	restore := openBrowser
	openBrowser = func(u string) error {
		go func() {
			if resp, err := http.Get(u); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	defer func() { openBrowser = restore }()
	if err := runLogin(context.Background(), options{mode: "login", remoteURL: f.mcpURL(), tokenDir: tokenDir, headers: map[string]string{}}); err != nil {
		t.Fatalf("login: %v", err)
	}

	// terva's REAL client spawns the bridge as an ordinary stdio MCP command.
	cl, err := mcp.Start(context.Background(), "remote", mcp.ServerConfig{
		Command: bridgeBin,
		Args:    []string{"--token-dir", tokenDir, f.mcpURL()},
	}, "", os.Stderr)
	if err != nil {
		t.Fatalf("terva mcp.Start(bridge): %v", err)
	}
	defer cl.Stop()

	// The remote's tool is exposed 1:1 through the bridge.
	tools := cl.Tools()
	if len(tools) != 1 || tools[0].Name != "remote_echo" {
		t.Fatalf("terva saw tools %+v, want the remote's remote_echo through the bridge", tools)
	}

	// And a tools/call reaches the OAuth-gated remote and comes back.
	res, err := cl.CallTool(context.Background(), "remote_echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool through the bridge: %v", err)
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "called remote_echo") {
		t.Errorf("tool result through the bridge = %+v, want the remote's response", res)
	}
}

func buildBridge(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "terva-mcp-bridge")
	if runtime.GOOS == "windows" {
		out += ".exe" // Windows exec needs the extension; go build writes it there
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v\n%s", err, b)
	}
	return out
}
