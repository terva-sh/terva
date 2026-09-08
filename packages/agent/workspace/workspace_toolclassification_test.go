package workspace

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func TestRebuildRevokesRemovedToolClassification(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")
	s := newAskSession(t, "classification-rebuild")
	pol := permissions.NewPolicy(core.ApprovalWorkspace, nil)
	pol.ReadOnly.Add("removed_extension_tool")
	s.agent.ReadOnly = pol.ReadOnly
	s.gate = core.NewPolicyGate(pol, nil)
	s.rebuildTools("extension-reload")
	if s.agent.ReadOnly.Has("removed_extension_tool") {
		t.Fatal("rebuild retained read-only authority for a removed extension tool")
	}
}

// The test binary acts as either protocol peer, so reload coverage also runs
// on Windows without a shell or an extra language runtime.
//
// It identifies itself from its OWN argv, the way TestHelperConnector and
// TestBlockedExtensionHelper already do. It used to key off a
// TERVA_G3_BACKEND_HELPER variable that the parent set process-wide, which
// worked only because Go runs this file's tests in source order: this helper
// had already returned before the parent set the marker. That ordering is the
// whole guarantee, and t.Parallel below removes it. A helper that reads global
// state could then wake mid-suite, read someone else's trailing arguments and
// block on stdin. flag.Args() is empty in an ordinary run and no other test
// can set it.
func TestToolGenerationBackendHelper(t *testing.T) {
	args := flag.Args()
	if len(args) != 2 {
		return
	}
	protocol, path := args[0], args[1]
	state, err := os.ReadFile(path)
	if err != nil {
		os.Exit(2)
	}
	readOnly := string(state) == "read"
	enc := json.NewEncoder(os.Stdout)
	if protocol == "ext" {
		_ = enc.Encode(map[string]any{"type": "hello", "name": "generation", "version": "1", "capabilities": []string{"tools"}})
		if string(state) != "removed" {
			_ = enc.Encode(map[string]any{"type": "register_tool", "name": "generation_probe", "description": "probe", "schema": map[string]any{"type": "object"}, "read_only": readOnly})
		}
		_ = enc.Encode(map[string]any{"type": "ready"})
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			Type, Method string
			ID           json.RawMessage
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(3)
		}
		if protocol == "ext" {
			switch req.Type {
			case "shutdown":
				_ = enc.Encode(map[string]any{"type": "shutdown_ack"})
				os.Exit(0)
			case "tool_call":
				_ = enc.Encode(map[string]any{"type": "tool_result", "id": req.ID, "content": []map[string]any{{"type": "text", "text": string(state)}}})
			}
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "generation", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "generation_probe", "description": "probe", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": readOnly}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(state)}}}
		default:
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestRebuildToolsClassificationAcrossBackendReload(t *testing.T) {
	// Set once here rather than inside each subtest, which is what lets the six
	// run at once. Both variables are process-wide, so a per-subtest t.Setenv
	// makes t.Parallel panic and the subtests were serialised at about 4.7s
	// each. Go permits a non-parallel parent to call t.Setenv and still have
	// parallel subtests, and the parent's value stays live until the last of
	// them finishes, because the parent's cleanup runs after they join.
	//
	// Sharing them is sound because a subtest needs AN empty TERVA_HOME, not its
	// own. Only build.Resolve inside newAskSession reads these, and everything a
	// subtest writes goes to its own dir below, which extensions.New and
	// mcp.StartAll are handed explicitly.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-test-key")

	for _, backend := range []string{"ext", "mcp"} {
		for _, mode := range []core.ApprovalMode{core.ApprovalWorkspace, core.ApprovalAutoEdit, core.ApprovalPlan} {
			t.Run(backend+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				home := testsupport.TempDir(t)
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, "backend-state")
				argv := []string{"-test.run=^TestToolGenerationBackendHelper$", "--", backend, path}
				s := newAskSession(t, "backend-classification")
				s.args.CWD, s.args.Approval = home, string(mode)
				pol := permissions.NewPolicy(mode, nil)
				s.gate = core.NewPolicyGate(pol, nil)
				s.agent.ReadOnly = pol.ReadOnly
				// Two minutes rather than one. Six of these now run together, each
				// re-execing a race-instrumented binary five times, and a CI job can
				// be down to roughly one core under runner load. This is a failure
				// deadline and not a wait, so headroom costs nothing on the happy
				// path.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				name := "generation_probe"
				var refresh func(string)
				if backend == "ext" {
					dir := filepath.Join(home, "extension")
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
					manifest, _ := json.Marshal(map[string]any{"name": "generation", "exec": exe, "args": argv})
					if err := os.WriteFile(filepath.Join(dir, "extension.json"), manifest, 0o600); err != nil {
						t.Fatal(err)
					}
					mgr := extensions.New(home, home, "test", "fake", "fake", nil)
					s.extMgr = mgr
					t.Cleanup(func() { mgr.Stop(time.Second) })
					loaded := false
					mgr.SetOnReload(func() { s.rebuildTools("extension-reload") })
					refresh = func(string) {
						if loaded {
							mgr.Reload(ctx, time.Second)
						} else {
							if errs := mgr.LoadExplicit(ctx, []string{dir}); len(errs) != 0 {
								t.Fatal(errs)
							}
							mgr.WaitForReady(testsupport.ExtReadyGrace)
							loaded = true
							s.rebuildTools("extension-reload")
						}
					}
				} else {
					mgr := mcp.StartAll(ctx, nil, home, nil)
					t.Cleanup(mgr.StopAll)
					s.ws.mcpAdapter = &build.MCPToolAdapter{Mgr: mgr}
					name = mcp.NamespaceTool("generation", name)
					refresh = func(state string) {
						mgr.StopOne("generation")
						if state != "removed" {
							if err := mgr.StartOne(ctx, "generation", mcp.ServerConfig{Command: exe, Args: argv}, nil); err != nil {
								t.Fatal(err)
							}
						}
						s.rebuildTools("mcp-toggle")
					}
				}
				var previous core.Tool
				for _, state := range []string{"read", "mutate", "removed", "mutate", "read"} {
					if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
						t.Fatal(err)
					}
					refresh(state)
					if previous != nil {
						res, err := previous.Execute(ctx, json.RawMessage(`{}`), nil)
						if err == nil && !res.IsError {
							t.Fatal("old wrapper executed against a replacement backend")
						}
					}
					callCtx, tool, exists := s.agent.ToolForCall(ctx, name)
					wantExists := state != "removed" && (mode != core.ApprovalPlan || state == "read")
					if exists != wantExists {
						t.Fatalf("%s: registered=%v want=%v", state, exists, wantExists)
					}
					allowed, _, _ := s.gate.Check(callCtx, name, nil, "", "")
					if allowed != (state == "read") {
						t.Fatalf("%s: allowed=%v", state, allowed)
					}
					if allowed {
						res, err := tool.Execute(callCtx, json.RawMessage(`{}`), nil)
						if err != nil || res.IsError {
							t.Fatalf("current read failed: %v %+v", err, res)
						}
					}
					previous = tool
				}
			})
		}
	}
}
