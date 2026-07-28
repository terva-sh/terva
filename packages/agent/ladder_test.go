package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// buildLadderHookStub compiles the hooks package's stub for ladder
// integration tests.
func buildLadderHookStub(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "hookstub")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./hooks/testdata/cmd/hookstub")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hookstub: %v\n%s", err, b)
	}
	return out
}

func ladderEngine(t *testing.T, mode string) *hooks.Engine {
	t.Helper()
	stub := buildLadderHookStub(t)
	e := hooks.NewEngine(hooks.Config{PreToolUse: []hooks.Spec{{Command: stub, Args: []string{mode}}}}, testsupport.TempDir(t), t.Logf)
	if e == nil {
		t.Fatal("nil engine")
	}
	return e
}

func callBash(fn func(provider.ToolCallBlock) (bool, string, json.RawMessage)) (bool, string, json.RawMessage) {
	return fn(provider.ToolCallBlock{ID: "T1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)})
}

func TestLadderHookDenyRefusesBeforeGate(t *testing.T) {
	fn := build.BuildBeforeToolExecute(context.Background(), ladderEngine(t, "deny"), nil, nil)
	allowed, reason, _ := callBash(fn)
	if allowed {
		t.Fatal("hook deny must refuse")
	}
	if !strings.Contains(reason, "stub says no") {
		t.Errorf("reason should come from the hook, got %q", reason)
	}
}

func TestLadderHookAllowSkipsRefusingGate(t *testing.T) {
	// A refusing gate (nil inner, ask policy) would block this call;
	// the user hook's allow is final and skips it.
	gate := core.NewConfirmGate(nil)
	fn := build.BuildBeforeToolExecute(context.Background(), ladderEngine(t, "allow"), gate, nil)
	if allowed, reason, _ := callBash(fn); !allowed {
		t.Fatalf("hook allow should skip the gate, got refusal %q", reason)
	}
	// Sanity: without the hook the same gate refuses.
	fn = build.BuildBeforeToolExecute(context.Background(), nil, gate, nil)
	if allowed, _, _ := callBash(fn); allowed {
		t.Fatal("control: gate alone should refuse")
	}
}

func TestLadderHookRewriteFlowsToModifiedArgs(t *testing.T) {
	fn := build.BuildBeforeToolExecute(context.Background(), ladderEngine(t, "rewrite"), nil, nil)
	allowed, _, modified := callBash(fn)
	if !allowed {
		t.Fatal("rewrite-only hook must not block")
	}
	if !strings.Contains(string(modified), "echo rewritten") {
		t.Errorf("rewritten args should surface as modifiedArgs, got %s", modified)
	}
}

func TestLadderGateSeesRewrittenArgs(t *testing.T) {
	// The gate evaluates post-rewrite args: a deny rule matching the
	// ORIGINAL command must not fire once the hook rewrote it away —
	// and one matching the REWRITTEN command must.
	withTempHome(t)
	mkGate := func(pattern string) *core.ConfirmGate {
		pol := &core.PermissionPolicy{
			Mode:     core.ApprovalYolo,
			Rules:    []core.PermissionRule{{Tool: "bash", Args: regexp.MustCompile(pattern), Decision: core.RuleDeny, Source: "user"}},
			ReadOnly: permissions.BuiltinReadOnlySet(), EditTools: permissions.EditToolSet(),
		}
		return core.NewPolicyGate(pol, nil)
	}
	fn := build.BuildBeforeToolExecute(context.Background(), ladderEngine(t, "rewrite"), mkGate(`^ls$`), nil)
	if allowed, _, _ := callBash(fn); !allowed {
		t.Error("deny rule on the original args must not fire after rewrite")
	}
	fn = build.BuildBeforeToolExecute(context.Background(), ladderEngine(t, "rewrite"), mkGate(`^echo rewritten$`), nil)
	if allowed, _, _ := callBash(fn); allowed {
		t.Error("deny rule on the rewritten args must fire")
	}
}

func TestBuildHookEngineFromConfig(t *testing.T) {
	home := withTempHome(t)
	stub := buildLadderHookStub(t)
	cfg := config.Config{Hooks: &hooks.Config{PreToolUse: []hooks.Spec{{Command: stub, Args: []string{"deny"}}}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// Untrusted (false): user-config hooks still build — only PROJECT hooks
	// are trust-gated. Proves the user layer is unaffected by the trust gate.
	eng := build.BuildHookEngine(build.Args{CWD: testsupport.TempDir(t)}, false)
	if eng == nil {
		t.Fatal("engine should build from config")
	}
	// Release the hooks.log handle before the temp home is removed —
	// Windows can't unlink a file that is still open, which fails the
	// t.TempDir cleanup.
	defer eng.Close()
	if r := eng.RunPre(context.Background(), "bash", []byte(`{}`)); r.Decision != hooks.DecisionDeny {
		t.Errorf("configured hook should deny, got %q", r.Decision)
	}
}
