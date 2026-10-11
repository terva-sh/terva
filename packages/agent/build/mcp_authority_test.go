//go:build !terva_no_mcp_http

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// remoteMCP serves a minimal Streamable-HTTP MCP endpoint whose tools/list
// answers tools, a JSON array, and returns its URL.
func remoteMCP(t *testing.T, tools string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := `{}`
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"remote","version":"1"}}`
		case "tools/list":
			result = `{"tools":` + tools + `}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, result)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// mcpModeWant is what one approval mode does with one MCP tool: whether the
// merge registers it, and the policy's verdict on a call that arrives anyway.
type mcpModeWant struct {
	registered bool
	verdict    permission.PolicyVerdict
}

// checkMCPModes merges adapter's tools under each mode, as the startup merge
// and the live mode switch do, and checks every tool want names.
func checkMCPModes(t *testing.T, adapter *MCPToolAdapter, want map[permission.ApprovalMode]map[string]mcpModeWant) {
	t.Helper()
	for mode, tools := range want {
		reg := core.Registry{}
		pol := permissions.NewPolicy(mode, nil)
		MergeToolsForMode(reg, mode, pol.ReadOnly, adapter)
		for name, w := range tools {
			if _, ok := reg[name]; ok != w.registered {
				t.Errorf("%s/%s: registered = %v, want %v", mode, name, ok, w.registered)
			}
			if v, _ := pol.Evaluate(name, json.RawMessage(`{}`)); v != w.verdict {
				t.Errorf("%s/%s: verdict = %s, want %s", mode, name, verdictName(v), verdictName(w.verdict))
			}
		}
	}
}

// verdictName spells a verdict for a failure message.
func verdictName(v permission.PolicyVerdict) string {
	switch v {
	case permission.VerdictAllow:
		return "allow"
	case permission.VerdictAsk:
		return "ask"
	case permission.VerdictDeny:
		return "deny"
	}
	return fmt.Sprintf("verdict(%d)", v)
}

// 🚨 A read-only tool on a remote MCP server reads nothing on this machine, yet
// every call sends the model's arguments to that server. As a local read it
// ran unasked in workspace and auto-edit and stayed in plan mode. Decision 0005
// gives it network-read, which asks. A tool that claims destructiveHint as
// well is not read-only at all.
func TestRemoteReadOnlyMCPToolIsNetworkRead(t *testing.T) {
	url := remoteMCP(t, `[
		{"name":"search","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},
		{"name":"wipe","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"destructiveHint":true}}
	]`)
	mgr := mcp.StartAll(context.Background(), &mcp.Config{Servers: map[string]mcp.ServerConfig{"remote": {Transport: "http", URL: url}}}, "", nil)
	defer mgr.StopAll()
	adapter := &MCPToolAdapter{Mgr: mgr}

	got := map[string]ExtensionToolInfo{}
	for _, info := range adapter.Tools() {
		got[info.Name] = info
	}
	if len(got) != 2 {
		t.Fatalf("tools = %+v (warnings %v), want search and wipe", got, mgr.Warnings())
	}
	if a := got["mcp_remote_search"].Authority; a != string(permission.AuthNetworkRead) {
		t.Errorf("remote read-only tool authority = %q, want network-read", a)
	}
	if w := got["mcp_remote_wipe"]; w.ReadOnly || w.Authority != "" {
		t.Errorf("a tool with both hints = ReadOnly %v, Authority %q, want neither", w.ReadOnly, w.Authority)
	}

	const search, wipe = "mcp_remote_search", "mcp_remote_wipe"
	refused := mcpModeWant{registered: false, verdict: permission.VerdictDeny}
	asks := mcpModeWant{registered: true, verdict: permission.VerdictAsk}
	runs := mcpModeWant{registered: true, verdict: permission.VerdictAllow}
	checkMCPModes(t, adapter, map[permission.ApprovalMode]map[string]mcpModeWant{
		permission.ApprovalPlan:      {search: refused, wipe: refused},
		permission.ApprovalAutoEdit:  {search: asks, wipe: asks},
		permission.ApprovalWorkspace: {search: asks, wipe: asks},
		permission.ApprovalAsk:       {search: asks, wipe: asks},
		permission.ApprovalYolo:      {search: runs, wipe: runs},
	})

	// The allow rule docs/mcp.md shows trusts the tool where it would ask. Plan
	// refuses before any rule runs, so the rule does not bring it back there.
	trust := []permission.PermissionRule{{Tool: search, Decision: permission.RuleAllow, Source: "user"}}
	for mode, want := range map[permission.ApprovalMode]permission.PolicyVerdict{
		permission.ApprovalWorkspace: permission.VerdictAllow,
		permission.ApprovalAutoEdit:  permission.VerdictAllow,
		permission.ApprovalPlan:      permission.VerdictDeny,
	} {
		pol := permissions.NewPolicy(mode, trust)
		MergeToolsForMode(core.Registry{}, mode, pol.ReadOnly, adapter)
		if v, _ := pol.Evaluate(search, nil); v != want {
			t.Errorf("%s with an allow rule: verdict = %s, want %s", mode, verdictName(v), verdictName(want))
		}
	}
}

// buildMCPStub builds the stdio stub server, or skips a short run.
func buildMCPStub(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and spawns the stdio stub")
	}
	stub := filepath.Join(testsupport.TempDir(t), "mcpstub")
	if runtime.GOOS == "windows" {
		stub += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", stub, "terva.sh/terva/packages/agent/mcp/testdata/cmd/mcpstub")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mcpstub: %v\n%s", err, b)
	}
	return stub
}

// A read-only tool on a stdio server keeps the local-read treatment, because
// installing the server was already consent to run it.
func TestStdioReadOnlyMCPToolStaysALocalRead(t *testing.T) {
	stub := buildMCPStub(t)
	mgr := mcp.StartAll(context.Background(), &mcp.Config{Servers: map[string]mcp.ServerConfig{"local": {Command: stub}}}, "", nil)
	defer mgr.StopAll()
	adapter := &MCPToolAdapter{Mgr: mgr}

	for _, info := range adapter.Tools() {
		if info.Authority != "" {
			t.Errorf("%s: stdio tool authority = %q, want none", info.Name, info.Authority)
		}
	}

	const peek, add = "mcp_local_peek", "mcp_local_add"
	refused := mcpModeWant{registered: false, verdict: permission.VerdictDeny}
	asks := mcpModeWant{registered: true, verdict: permission.VerdictAsk}
	runs := mcpModeWant{registered: true, verdict: permission.VerdictAllow}
	checkMCPModes(t, adapter, map[permission.ApprovalMode]map[string]mcpModeWant{
		permission.ApprovalPlan:      {peek: runs, add: refused},
		permission.ApprovalAutoEdit:  {peek: runs, add: asks},
		permission.ApprovalWorkspace: {peek: runs, add: asks},
		permission.ApprovalAsk:       {peek: asks, add: asks},
		permission.ApprovalYolo:      {peek: runs, add: runs},
	})
}

// 🚨 A stdio server that marks itself as a relay, as terva-mcp-bridge does,
// sends each call on to a remote server. Its read-only tool is network-read,
// the same as on an http server. Without the mark it ran unasked in workspace
// and auto-edit and stayed in plan, although every call left the machine.
func TestMarkedStdioRelayReadOnlyMCPToolIsNetworkRead(t *testing.T) {
	stub := buildMCPStub(t)
	mgr := mcp.StartAll(context.Background(), &mcp.Config{Servers: map[string]mcp.ServerConfig{"relay": {Command: stub, Args: []string{"--remote-mark"}}}}, "", nil)
	defer mgr.StopAll()
	adapter := &MCPToolAdapter{Mgr: mgr}

	got := map[string]ExtensionToolInfo{}
	for _, info := range adapter.Tools() {
		got[info.Name] = info
	}
	const peek, add = "mcp_relay_peek", "mcp_relay_add"
	p, ok := got[peek]
	if !ok || !p.ReadOnly {
		t.Fatalf("no read-only %s among %d tools (warnings %v)", peek, len(got), mgr.Warnings())
	}
	if p.Authority != string(permission.AuthNetworkRead) {
		t.Fatalf("marked relay read-only tool authority = %q, want network-read", p.Authority)
	}
	// The mark changes nothing for a tool that is not read-only.
	if a := got[add]; a.ReadOnly || a.Authority != "" {
		t.Errorf("marked relay mutating tool = ReadOnly %v, Authority %q, want neither", a.ReadOnly, a.Authority)
	}

	refused := mcpModeWant{registered: false, verdict: permission.VerdictDeny}
	asks := mcpModeWant{registered: true, verdict: permission.VerdictAsk}
	runs := mcpModeWant{registered: true, verdict: permission.VerdictAllow}
	checkMCPModes(t, adapter, map[permission.ApprovalMode]map[string]mcpModeWant{
		permission.ApprovalPlan:      {peek: refused, add: refused},
		permission.ApprovalAutoEdit:  {peek: asks, add: asks},
		permission.ApprovalWorkspace: {peek: asks, add: asks},
		permission.ApprovalAsk:       {peek: asks, add: asks},
		permission.ApprovalYolo:      {peek: runs, add: runs},
	})
}
