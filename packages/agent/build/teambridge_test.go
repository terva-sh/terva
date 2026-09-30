package build

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
	"testing"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// buildTeamBridge builds the mcpbridge test binary, which serves the bridge's
// tools when run as `bridge mcp-talkoot-bridge --socket P`, and points
// tervaExecutable at it.
func buildTeamBridge(t *testing.T) {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "bridge")
	cmd := exec.Command("go", "build", "-o", out, "terva.sh/terva/packages/agent/mcpbridge/testdata/cmd/bridge")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v\n%s", err, b)
	}
	old := tervaExecutable
	tervaExecutable = func() (string, error) { return out, nil }
	t.Cleanup(func() { tervaExecutable = old })
}

// serveTeamStub answers each call on the team socket with the tool's name, and
// sends the request it read on got.
func serveTeamStub(t *testing.T, path string) <-chan mcpbridge.TeamRequest {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan mcpbridge.TeamRequest, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req mcpbridge.TeamRequest
			if json.Unmarshal(bytes.TrimSpace(line), &req) == nil {
				got <- req
				reply, _ := json.Marshal(mcpbridge.TeamReply{Text: "answered " + req.Tool})
				_, _ = c.Write(append(reply, '\n'))
			}
			_ = c.Close()
		}
	}()
	return got
}

// A --team-socket run starts the Talkoot bridge as its one MCP server, and a
// planning member still gets the bridge's send and handoff tools. A server in
// the user's config does not start beside it.
func TestATeamRunStartsTheBridgeAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns the bridge subprocess")
	}
	buildTeamBridge(t)
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	other := filepath.Join(testsupport.TempDir(t), "does-not-exist")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"mcp":{"servers":{"other":{"command":`+jsonQuote(other)+`}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(testsupport.SocketDir(t), "w.tk")
	got := serveTeamStub(t, sock)

	r := &Resolved{CWD: home, ToolRegistry: core.Registry{}, ApprovalMode: permission.ApprovalPlan}
	adapter, stop := SetupMCP(context.Background(), Args{CWD: home, TeamSocket: sock}, r)
	defer stop()
	if err := adapter.TeamBridgeReady(); err != nil {
		t.Fatalf("bridge not ready: %v", err)
	}
	if st := adapter.Mgr.Status(); len(st) != 1 || st[0].Name != mcpbridge.TeamServerName {
		t.Fatalf("servers = %+v, want the bridge alone", st)
	}
	if w := adapter.Mgr.Warnings(); len(w) != 0 {
		t.Fatalf("warnings = %v, want none: the user's server must not start", w)
	}
	for _, name := range permissions.TeamBridgeTools() {
		if _, ok := r.ToolRegistry[name]; !ok {
			t.Errorf("plan-mode registry lacks %s", name)
		}
	}

	send := r.ToolRegistry[permissions.TeamBridgeTools()[0]]
	res, err := send.Execute(context.Background(), json.RawMessage(`{"to":["helm"],"body":"Hi."}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(provider.TextBlock).Text; res.IsError || text != "answered talkoot_send" {
		t.Fatalf("result = %+v, want the orchestrator's answer", res)
	}
	if req := <-got; req.Tool != "talkoot_send" || !strings.Contains(string(req.Input), `"Hi."`) {
		t.Fatalf("orchestrator saw %+v", req)
	}
}

// 🚨 A bridge that did not start leaves the member unable to reach its team,
// and terva rpc refuses to run on that report.
func TestTeamBridgeReadyNeedsTheBridge(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	old := tervaExecutable
	tervaExecutable = func() (string, error) { return filepath.Join(testsupport.TempDir(t), "missing"), nil }
	defer func() { tervaExecutable = old }()

	r := &Resolved{ToolRegistry: core.Registry{}}
	adapter, stop := SetupMCP(context.Background(), Args{TeamSocket: filepath.Join(testsupport.TempDir(t), "w.tk")}, r)
	defer stop()
	err := adapter.TeamBridgeReady()
	if err == nil || !strings.Contains(err.Error(), "did not list mcp_terva_talkoot_talkoot_send") {
		t.Fatalf("err = %v, want the missing tool named", err)
	}
	if err := (*MCPToolAdapter)(nil).TeamBridgeReady(); err == nil {
		t.Fatal("a run with no MCP adapter reported the bridge ready")
	}
}

// Plan mode admits a side-effecting tool only when the source marks it
// PlanKeep, which only the bridge's tools do.
func TestPlanKeepAdmitsATeamTool(t *testing.T) {
	send := ExtensionToolInfo{Name: "mcp_terva_talkoot_talkoot_send"}
	if ExtToolRegisters(send, permission.ApprovalPlan) {
		t.Fatal("plan admitted a side-effecting tool with no PlanKeep")
	}
	send.PlanKeep = true
	if !ExtToolRegisters(send, permission.ApprovalPlan) {
		t.Fatal("plan refused a PlanKeep tool")
	}
}

// A run without --team-socket keeps the person's servers, and --no-mcp still
// starts nothing.
func TestMCPServersForWithoutATeam(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"mcp":{"servers":{"other":{"command":"x"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, allowed, ok := mcpServersFor(Args{CWD: home}, &Resolved{})
	if !ok || cfg == nil || len(cfg.Servers) != 1 || allowed != nil {
		t.Fatalf("cfg = %+v allowed = %v ok = %v, want the user's server", cfg, allowed, ok)
	}
	if _, _, ok := mcpServersFor(Args{CWD: home, NoMCP: true}, &Resolved{}); ok {
		t.Fatal("--no-mcp started a manager")
	}
	cfg, allowed, ok = mcpServersFor(Args{CWD: home, NoMCP: true, TeamSocket: "/s.tk"}, &Resolved{})
	if !ok || len(cfg.Servers) != 1 || !allowed[mcpbridge.TeamServerName] {
		t.Fatalf("cfg = %+v allowed = %v ok = %v, want the bridge alone", cfg, allowed, ok)
	}
	if sc := cfg.Servers[mcpbridge.TeamServerName]; strings.Join(sc.Args, " ") != "mcp-talkoot-bridge --socket /s.tk" {
		t.Fatalf("bridge args = %v", sc.Args)
	}
	// A question waits for a person, so the call bound is not the minute a
	// tool call gets by default.
	if sc := cfg.Servers[mcpbridge.TeamServerName]; sc.TimeoutMS != mcpbridge.TeamCallTimeoutMS {
		t.Fatalf("bridge timeout = %d ms, want %d", sc.TimeoutMS, mcpbridge.TeamCallTimeoutMS)
	}
}

// A team run advertises the bridge's tools from the first turn under lazy
// tool visibility, so a member need not activate its team verbs to reply.
func TestATeamRunKeepsTheBridgeGroupActive(t *testing.T) {
	configured := []string{"mcp:docs"}
	got := lazyToolActive(configured, Args{TeamSocket: "/s.tk"})
	if strings.Join(got, ",") != "mcp:docs,mcp:terva_talkoot" {
		t.Fatalf("active groups = %v", got)
	}
	if len(configured) != 1 {
		t.Fatalf("the configured list changed: %v", configured)
	}
	if got := lazyToolActive(configured, Args{}); strings.Join(got, ",") != "mcp:docs" {
		t.Fatalf("a run without the bridge = %v", got)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// 🔑 A rebuild merges a team run's bridge before the extensions, so no
// extension tool can take a bridge tool's name and the policy's pre-approval
// with it. Without a bridge, an extension still wins a collision against MCP.
func TestARebuildMergesTheBridgeFirst(t *testing.T) {
	ext := &extensions.Manager{}
	team := &MCPToolAdapter{team: true}
	got := LiveToolSet{Ext: ext, MCP: team}.toolSources()
	if len(got) != 2 || got[0] != ExtensionToolSource(team) {
		t.Fatalf("team run order = %T first, want the bridge", got[0])
	}
	plain := &MCPToolAdapter{}
	got = LiveToolSet{Ext: ext, MCP: plain}.toolSources()
	if len(got) != 2 || got[1] != ExtensionToolSource(plain) {
		t.Fatalf("plain run order = %T last, want MCP after the extensions", got[1])
	}
	if _, ok := got[0].(*ExtToolAdapter); !ok {
		t.Fatalf("plain run order = %T first, want the extensions", got[0])
	}
}

// The merge is first-write-wins, so whichever source goes first owns a name.
// This is what the order above buys.
func TestTheFirstSourceOwnsABridgeName(t *testing.T) {
	name := permissions.TeamBridgeTools()[0]
	reg := core.Registry{}
	MergeToolsForMode(reg, permission.ApprovalAsk, nil, fakeSource{name: name, desc: "the bridge"})
	MergeToolsForMode(reg, permission.ApprovalAsk, nil, fakeSource{name: name, desc: "an extension"})
	if d := reg[name].Description(); d != "the bridge" {
		t.Fatalf("%s = %q, want the first source's tool", name, d)
	}
}

type fakeSource struct{ name, desc string }

func (f fakeSource) Tools() []ExtensionToolInfo {
	return []ExtensionToolInfo{{Name: f.name, Description: f.desc, tool: fakeTool{name: f.name, desc: f.desc, schema: `{"type":"object"}`}}}
}
func (f fakeSource) NewExtensionTool(info ExtensionToolInfo) core.Tool { return info.tool }

// A team run asks through the bridge's ask_user_question, so the native one,
// which has no channel in rpc, leaves the registry. Other runs keep it.
func TestATeamRunDropsTheNativeQuestionTool(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	if reg := BuildToolRegistry(Args{TeamSocket: "/s.tk"}, permission.ApprovalAsk, dir, nil, "anthropic", "", false, nil); reg["ask_user_question"] != nil {
		t.Fatal("a team run kept the native ask_user_question")
	}
	if reg := BuildToolRegistry(Args{}, permission.ApprovalAsk, dir, nil, "anthropic", "", false, nil); reg["ask_user_question"] == nil {
		t.Fatal("a plain run lost ask_user_question")
	}
}
