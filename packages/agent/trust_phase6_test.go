package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/mcp"
)

// Workspace Trust Phase 6: a TRUSTED project may define hooks and MCP
// servers; an UNtrusted one may not. These tests are the security gate:
// they prove an untrusted project's hooks never reach the engine and its
// MCP servers never spawn, while the user-config layer is unaffected.

// writeUserHooks writes a user-config hooks block (always trusted) using
// the given hookstub mode.
func writeUserHooks(t *testing.T, home, stub, mode string) {
	t.Helper()
	cfg := Config{Hooks: &hooks.Config{PreToolUse: []hooks.Spec{{Command: stub, Args: []string{mode}}}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// (writeProjectConfig and jsonString are shared helpers defined in
// config_test.go.)

// buildPhase6MCPStub compiles the mcp package's stub server for these
// agent-level integration tests.
func buildPhase6MCPStub(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mcpstub")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./mcp/testdata/cmd/mcpstub")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mcpstub: %v\n%s", err, b)
	}
	return out
}

// jsonHooksConfig renders a hooks block JSON snippet for a project config,
// pointing at the hookstub in the given mode.
func jsonHooksConfig(stub, mode string) string {
	spec := hooks.Config{PreToolUse: []hooks.Spec{{Command: stub, Args: []string{mode}}}}
	b, _ := json.Marshal(map[string]any{"hooks": spec})
	return string(b)
}

// TestUntrustedProjectHooksDoNotFire is the headline security assertion:
// a project with hooks in .terva/config.json gets NO hook engine when the
// workspace is untrusted (the default). The project hook must never run.
func TestUntrustedProjectHooksDoNotFire(t *testing.T) {
	withTempHome(t)
	stub := buildLadderHookStub(t)
	proj := t.TempDir()
	// Project declares a DENY hook — if it leaked it would block every call.
	writeProjectConfig(t, proj, jsonHooksConfig(stub, "deny"))

	// Untrusted (default): no user hooks, project hooks gated → nil engine.
	eng := buildHookEngine(Args{CWD: proj}, false)
	if eng != nil {
		defer eng.Close()
		// If an engine exists at all here, the project hook leaked through.
		r := eng.RunPre(context.Background(), "bash", []byte(`{}`))
		t.Fatalf("untrusted project hook must NOT load; got an engine with decision %q", r.Decision)
	}
}

// TestTrustedProjectHooksFire proves a trusted project's hook runs. Trust
// is provided two ways the resolver honors: the --trust flag and the store.
func TestTrustedProjectHooksFire(t *testing.T) {
	withTempHome(t)
	stub := buildLadderHookStub(t)
	proj := t.TempDir()
	writeProjectConfig(t, proj, jsonHooksConfig(stub, "deny"))

	run := func(t *testing.T, trusted bool) {
		eng := buildHookEngine(Args{CWD: proj}, trusted)
		if eng == nil {
			t.Fatal("trusted project hook should build an engine")
		}
		defer eng.Close()
		if r := eng.RunPre(context.Background(), "bash", []byte(`{}`)); r.Decision != hooks.DecisionDeny {
			t.Errorf("trusted project hook should deny, got %q", r.Decision)
		}
	}

	// Via the --trust verdict (resolveTrust honors the flag; here the host
	// passes the resolved verdict straight to buildHookEngine).
	t.Run("flag", func(t *testing.T) { run(t, true) })

	// Via the persisted store: TrustPath + resolveTrustState resolves true.
	t.Run("store", func(t *testing.T) {
		if err := TrustPath(proj, false); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = UntrustPath(proj) })
		trusted := resolveTrustState(Args{CWD: proj}).IsTrusted()
		if !trusted {
			t.Fatal("store entry should resolve the workspace as trusted")
		}
		run(t, trusted)
	})
}

// TestHookMergeUnionUserAndProject proves the merge semantics: BOTH user and
// project hooks are present (append/union), user first. A user ALLOW plus a
// project DENY both load; the engine runs user-first, so the user allow is the
// decisive answer (allow stops the chain) — which is only observable if both
// specs were merged in the right order.
func TestHookMergeUnionUserAndProject(t *testing.T) {
	withTempHome(t)
	home := os.Getenv("TERVA_HOME")
	stub := buildLadderHookStub(t)
	writeUserHooks(t, home, stub, "allow")
	proj := t.TempDir()
	writeProjectConfig(t, proj, jsonHooksConfig(stub, "deny"))

	// Trusted: merged engine has user(allow) THEN project(deny).
	user, _ := LoadConfig()
	merged := mergeHookConfigs(user.Hooks, trustedProjectHooks(proj, true))
	if merged == nil || len(merged.PreToolUse) != 2 {
		t.Fatalf("merged hooks should hold both user and project specs, got %+v", merged)
	}
	// User spec is first (allow), project second (deny): union order.
	if got := merged.PreToolUse[0].Args; len(got) == 0 || got[0] != "allow" {
		t.Errorf("user hook must come first in the union, got %+v", merged.PreToolUse)
	}
	if got := merged.PreToolUse[1].Args; len(got) == 0 || got[0] != "deny" {
		t.Errorf("project hook must come second in the union, got %+v", merged.PreToolUse)
	}

	// Untrusted: only the user spec survives (project dropped).
	mergedOff := mergeHookConfigs(user.Hooks, trustedProjectHooks(proj, false))
	if mergedOff == nil || len(mergedOff.PreToolUse) != 1 || mergedOff.PreToolUse[0].Args[0] != "allow" {
		t.Errorf("untrusted merge should keep ONLY the user hook, got %+v", mergedOff)
	}
}

// TestUserHooksUnaffectedByTrust proves the user-config hooks behave
// identically regardless of the trust verdict (a plain project, no project
// hooks): the user's hook fires both when trusted and untrusted.
func TestUserHooksUnaffectedByTrust(t *testing.T) {
	withTempHome(t)
	home := os.Getenv("TERVA_HOME")
	stub := buildLadderHookStub(t)
	writeUserHooks(t, home, stub, "deny")
	proj := t.TempDir() // no project config at all

	for _, trusted := range []bool{false, true} {
		eng := buildHookEngine(Args{CWD: proj}, trusted)
		if eng == nil {
			t.Fatalf("user hooks should load (trusted=%v)", trusted)
		}
		if r := eng.RunPre(context.Background(), "bash", []byte(`{}`)); r.Decision != hooks.DecisionDeny {
			t.Errorf("user hook should deny (trusted=%v), got %q", trusted, r.Decision)
		}
		eng.Close()
	}
}

// TestUntrustedProjectMCPNotStarted is the MCP security assertion: a project
// declaring an MCP server in .terva/config.json contributes NO servers when
// untrusted, so nothing is spawned and no project tool appears.
func TestUntrustedProjectMCPNotStarted(t *testing.T) {
	withTempHome(t)
	stub := buildPhase6MCPStub(t)
	proj := t.TempDir()
	body := `{"mcp":{"servers":{"repo":{"command":` + jsonString(stub) + `}}}}`
	writeProjectConfig(t, proj, body)

	// Untrusted: trustedProjectMCP returns nil, merge yields nothing. The
	// adapter is now always non-nil (so /mcp can live-enable a server later),
	// but an untrusted project's server must contribute NO tools.
	r := &Resolved{Trusted: false, CWD: proj}
	adapter, stop := setupMCP(context.Background(), Args{CWD: proj}, r)
	defer stop()
	if adapter != nil && len(adapter.Tools()) != 0 {
		t.Fatalf("untrusted project MCP must NOT start; got tools %+v", adapter.Tools())
	}
}

// TestTrustedProjectMCPStarts proves a trusted project's MCP server spawns and
// its (namespaced) tools appear; a user-config server still loads alongside,
// and the user wins a name collision.
func TestTrustedProjectMCPStarts(t *testing.T) {
	home := withTempHome(t)
	stub := buildPhase6MCPStub(t)
	proj := t.TempDir()

	// User config defines a server "shared"; project defines "repo" AND a
	// colliding "shared" — the user's "shared" must win.
	userCfg := Config{MCP: &mcp.Config{Servers: map[string]mcp.ServerConfig{
		"shared": {Command: stub},
	}}}
	ub, _ := json.Marshal(userCfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), ub, 0o644); err != nil {
		t.Fatal(err)
	}
	// Project server "repo" plus a colliding "shared" pointing at a bogus
	// command: if the project's "shared" won, that bogus server would replace
	// the working user one. The user must win, so "shared" tools still appear.
	bogus := filepath.Join(t.TempDir(), "does-not-exist")
	body := `{"mcp":{"servers":{` +
		`"repo":{"command":` + jsonString(stub) + `},` +
		`"shared":{"command":` + jsonString(bogus) + `}}}}`
	writeProjectConfig(t, proj, body)

	r := &Resolved{Trusted: true, CWD: proj}
	adapter, stop := setupMCP(context.Background(), Args{CWD: proj}, r)
	defer stop()
	if adapter == nil {
		t.Fatal("trusted project MCP should start and yield an adapter")
	}
	names := map[string]bool{}
	for _, ti := range adapter.Tools() {
		names[ti.Name] = true
	}
	// Project server "repo" loaded.
	if !names["mcp_repo_add"] {
		t.Errorf("trusted project MCP server tools missing; got %v", names)
	}
	// User "shared" won the collision (its stub spawns → tools present).
	if !names["mcp_shared_add"] {
		t.Errorf("user MCP server should win the name collision and load; got %v", names)
	}
}

// TestUserMCPUnaffectedByUntrustedProject proves the user's MCP servers load
// regardless of an untrusted project: an untrusted project's servers are
// dropped but the user's are unchanged.
func TestUserMCPUnaffectedByUntrustedProject(t *testing.T) {
	home := withTempHome(t)
	stub := buildPhase6MCPStub(t)
	userCfg := Config{MCP: &mcp.Config{Servers: map[string]mcp.ServerConfig{
		"u": {Command: stub},
	}}}
	ub, _ := json.Marshal(userCfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), ub, 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	body := `{"mcp":{"servers":{"repo":{"command":` + jsonString(stub) + `}}}}`
	writeProjectConfig(t, proj, body)

	// Untrusted: only the user's "u" server tools appear; no "repo" tools.
	r := &Resolved{Trusted: false, CWD: proj}
	adapter, stop := setupMCP(context.Background(), Args{CWD: proj}, r)
	defer stop()
	if adapter == nil {
		t.Fatal("user MCP server should load even with an untrusted project")
	}
	names := map[string]bool{}
	for _, ti := range adapter.Tools() {
		names[ti.Name] = true
	}
	if !names["mcp_u_add"] {
		t.Errorf("user MCP server should still load; got %v", names)
	}
	if names["mcp_repo_add"] {
		t.Errorf("untrusted project MCP server must NOT load; got %v", names)
	}
}

// TestMergeMCPConfigsUserWinsCollision is a focused unit check of the merge
// rule independent of subprocess spawning.
func TestMergeMCPConfigsUserWinsCollision(t *testing.T) {
	user := &mcp.Config{Servers: map[string]mcp.ServerConfig{
		"shared": {Command: "user-cmd"},
		"only-u": {Command: "u"},
	}}
	project := &mcp.Config{Servers: map[string]mcp.ServerConfig{
		"shared": {Command: "project-cmd"}, // must lose to the user entry
		"only-p": {Command: "p"},
	}}
	merged := mergeMCPConfigs(user, project)
	if merged == nil {
		t.Fatal("merge of two non-empty configs should be non-nil")
	}
	if got := merged.Servers["shared"].Command; got != "user-cmd" {
		t.Errorf("user must win a name collision, got %q", got)
	}
	if _, ok := merged.Servers["only-p"]; !ok {
		t.Error("a trusted project may ADD a new server name")
	}
	if _, ok := merged.Servers["only-u"]; !ok {
		t.Error("user-only server must survive the merge")
	}
	// nil project (untrusted gate already applied) keeps only user servers.
	onlyUser := mergeMCPConfigs(user, nil)
	if onlyUser == nil || len(onlyUser.Servers) != 2 {
		t.Errorf("nil project merge should keep the 2 user servers, got %+v", onlyUser)
	}
	if mergeMCPConfigs(nil, nil) != nil {
		t.Error("merging two empty configs should be nil")
	}
}

// TestTrustedProjectHooksFlowThroughResolveVerdict ties the resolver to the
// loader: resolveTrustState(--trust) is what every call site passes to
// buildHookEngine, so a flag-trusted launch loads the project hook.
func TestTrustedProjectHooksFlowThroughResolveVerdict(t *testing.T) {
	withTempHome(t)
	stub := buildLadderHookStub(t)
	proj := t.TempDir()
	writeProjectConfig(t, proj, jsonHooksConfig(stub, "deny"))

	args := Args{CWD: proj, Trust: true}
	trusted := resolveTrustState(args).IsTrusted()
	if !trusted {
		t.Fatal("--trust must resolve to trusted")
	}
	eng := buildHookEngine(args, trusted)
	if eng == nil {
		t.Fatal("flag-trusted project hook should load")
	}
	defer eng.Close()
	if r := eng.RunPre(context.Background(), "bash", []byte(`{}`)); r.Decision != hooks.DecisionDeny {
		t.Errorf("flag-trusted project hook should deny, got %q", r.Decision)
	}
}
