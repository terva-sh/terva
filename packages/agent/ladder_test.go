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
	"terva.sh/terva/packages/core/permission"
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

// callBash asks g about a bash call the way the engine does. tool is the
// implementation the engine resolved for the call; nil stands for one that
// contributes no Preview.
func callBash(g core.Gate, tool core.Tool) (bool, string, json.RawMessage) {
	return g.CheckTool(context.Background(), provider.ToolCallBlock{ID: "T1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)}, tool)
}

func TestLadderHookDenyRefusesBeforeGate(t *testing.T) {
	fn := build.BuildToolGate(ladderEngine(t, "deny"), nil, nil)
	allowed, reason, _ := callBash(fn, nil)
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
	gate := permission.NewConfirmGate(nil)
	fn := build.BuildToolGate(ladderEngine(t, "allow"), gate, nil)
	if allowed, reason, _ := callBash(fn, nil); !allowed {
		t.Fatalf("hook allow should skip the gate, got refusal %q", reason)
	}
	// Sanity: without the hook the same gate refuses.
	fn = build.BuildToolGate(nil, gate, nil)
	if allowed, _, _ := callBash(fn, nil); allowed {
		t.Fatal("control: gate alone should refuse")
	}
}

func TestLadderHookRewriteFlowsToModifiedArgs(t *testing.T) {
	fn := build.BuildToolGate(ladderEngine(t, "rewrite"), nil, nil)
	allowed, _, modified := callBash(fn, nil)
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
	mkGate := func(pattern string) *permission.ConfirmGate {
		pol := &permission.PermissionPolicy{
			Mode:     permission.ApprovalYolo,
			Rules:    []permission.PermissionRule{{Tool: "bash", Args: regexp.MustCompile(pattern), Decision: permission.RuleDeny, Source: "user"}},
			ReadOnly: permissions.BuiltinReadOnlySet(), EditTools: permissions.EditToolSet(),
		}
		return permission.NewPolicyGate(pol, nil)
	}
	fn := build.BuildToolGate(ladderEngine(t, "rewrite"), mkGate(`^ls$`), nil)
	if allowed, _, _ := callBash(fn, nil); !allowed {
		t.Error("deny rule on the original args must not fire after rewrite")
	}
	fn = build.BuildToolGate(ladderEngine(t, "rewrite"), mkGate(`^echo rewritten$`), nil)
	if allowed, _, _ := callBash(fn, nil); allowed {
		t.Error("deny rule on the rewritten args must fire")
	}
}

// The §12.6 seam tests: a tool that contributes a Preview must have its
// contribution reach the confirmation prompt, and — the constraint §12.6
// calls out — the preview must be built from the SAME post-rewrite args the
// gate checks, not the original. A stale preview would approve one thing and
// run another.

// fakePreviewTool contributes its command field as the preview, the simplest
// contributor whose output reveals which args it was built from.
type fakePreviewTool struct{ name string }

func (f fakePreviewTool) Name() string        { return f.name }
func (f fakePreviewTool) Description() string { return "" }
func (f fakePreviewTool) Schema() json.RawMessage {
	return json.RawMessage(`{}`)
}
func (f fakePreviewTool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
func (f fakePreviewTool) Preview(args json.RawMessage, maxLen int) string {
	var m struct {
		Command string `json:"command"`
	}
	json.Unmarshal(args, &m)
	return "run: " + m.Command
}

// captureConfirmer records the preview each call is gated with, then allows
// it, so a test can read back what the prompt would have shown.
type captureConfirmer struct{ previews []string }

func (c *captureConfirmer) Confirm(ctx context.Context, toolName, preview string) permission.ConfirmDecision {
	c.previews = append(c.previews, preview)
	return permission.ConfirmDecision{Allow: true}
}

func TestLadderPreviewComesFromTheTool(t *testing.T) {
	withTempHome(t)
	conf := &captureConfirmer{}
	tool := fakePreviewTool{name: "bash"}
	fn := build.BuildToolGate(nil, permission.NewConfirmGate(conf), nil)
	allowed, _, _ := callBash(fn, tool)
	if !allowed {
		t.Fatal("capturing confirmer must allow")
	}
	if len(conf.previews) != 1 || conf.previews[0] != "run: ls" {
		t.Fatalf("previews = %v, want the tool's own contribution", conf.previews)
	}
}

func TestLadderPreviewFollowsTheRewrite(t *testing.T) {
	withTempHome(t)
	conf := &captureConfirmer{}
	tool := fakePreviewTool{name: "bash"}
	// The hook rewrites ls -> echo rewritten; the preview must describe what
	// will run, not what was asked.
	fn := build.BuildToolGate(ladderEngine(t, "rewrite"), permission.NewConfirmGate(conf), nil)
	allowed, _, _ := callBash(fn, tool)
	if !allowed {
		t.Fatal("capturing confirmer must allow")
	}
	if len(conf.previews) != 1 || conf.previews[0] != "run: echo rewritten" {
		t.Fatalf("preview = %v, want the REWRITTEN command, not the original", conf.previews)
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

// 🚨 A host's guard is ahead of the ladder, and its refusal is final. A
// hook's allow skips the gate, and it does not skip a guard.
func TestLadderGuardRefusesAheadOfAHooksAllow(t *testing.T) {
	asked := 0
	guard := func(_ context.Context, tool string, args json.RawMessage) string {
		asked++
		return "the guard says no to " + tool
	}
	fn := build.BuildToolGate(ladderEngine(t, "allow"), nil, nil, nil, build.CallGuard(guard))
	allowed, reason, _ := callBash(fn, nil)
	if allowed || reason != "the guard says no to bash" || asked != 1 {
		t.Fatalf("the guard must refuse before the hook runs, got allowed=%v reason=%q asked=%d", allowed, reason, asked)
	}
	// Control: the same ladder with a guard that has no word lets the call run.
	fn = build.BuildToolGate(ladderEngine(t, "allow"), nil, nil, build.CallGuard(func(context.Context, string, json.RawMessage) string { return "" }))
	if allowed, reason, _ := callBash(fn, nil); !allowed {
		t.Fatalf("a guard with no word must not refuse, got %q", reason)
	}
}

// 🚨 A hook can rewrite the arguments after a guard read them. The guard
// reads the rewrite, so a hook cannot turn a harmless call into one that
// the guard refuses.
func TestLadderGuardReadsAHooksRewrite(t *testing.T) {
	var seen []string
	guard := func(_ context.Context, _ string, args json.RawMessage) string {
		seen = append(seen, string(args))
		if strings.Contains(string(args), "echo rewritten") {
			return "the guard refuses the rewrite"
		}
		return ""
	}
	fn := build.BuildToolGate(ladderEngine(t, "rewrite"), nil, nil, build.CallGuard(guard))
	allowed, reason, _ := callBash(fn, nil)
	if allowed || reason != "the guard refuses the rewrite" {
		t.Fatalf("the guard must refuse the rewritten call, got allowed=%v reason=%q", allowed, reason)
	}
	if len(seen) != 2 || !strings.Contains(seen[0], `"ls"`) {
		t.Fatalf("the guard reads the call and then the rewrite, got %q", seen)
	}
}

// 🚨 A host's rung behind the ladder runs one time, for a call that every
// other rung allowed, with the arguments that the tool gets. A count that a
// host keeps there is a count of calls that run. A guard is the wrong place
// for a count: it runs again for a rewrite, and it runs for a call that a
// hook or the policy then refuses.
func TestLadderRungBehindRunsOnceForACallThatRuns(t *testing.T) {
	withTempHome(t)
	var seen []string
	after := build.AfterAllow(func(_ context.Context, tool string, args json.RawMessage) string {
		seen = append(seen, tool+" "+string(args))
		return ""
	})
	deny := func(pattern string) *permission.ConfirmGate {
		rules := []permission.PermissionRule{{Tool: "bash", Args: regexp.MustCompile(pattern), Decision: permission.RuleDeny, Source: "user"}}
		return permission.NewPolicyGate(permissions.NewPolicy(permission.ApprovalYolo, rules), nil)
	}
	// A hook rewrites the arguments. The rung reads the rewrite, one time,
	// and the tool gets the same arguments.
	fn := build.BuildToolGate(ladderEngine(t, "rewrite"), deny(`^never$`), nil, after)
	allowed, reason, mod := callBash(fn, nil)
	if !allowed || len(seen) != 1 || !strings.Contains(seen[0], "bash ") || !strings.Contains(seen[0], "echo rewritten") || !strings.Contains(string(mod), "echo rewritten") {
		t.Fatalf("the rung behind must read the rewrite one time: allowed=%v reason=%q seen=%q mod=%s", allowed, reason, seen, mod)
	}
	// With no rewrite it reads the call as the model made it.
	seen = nil
	fn = build.BuildToolGate(nil, deny(`^never$`), nil, after)
	if allowed, _, mod := callBash(fn, nil); !allowed || len(seen) != 1 || !strings.Contains(seen[0], `"ls"`) || mod != nil {
		t.Fatalf("the rung behind must read the call one time: allowed=%v seen=%q mod=%s", allowed, seen, mod)
	}
	// 🚨 A call that another rung refused does not reach it: a hook's deny,
	// a rule's deny, and a guard.
	seen = nil
	for name, fn := range map[string]core.Gate{
		"a hook's deny": build.BuildToolGate(ladderEngine(t, "deny"), nil, nil, after),
		"a rule's deny": build.BuildToolGate(nil, deny(`^ls$`), nil, after),
		"a guard": build.BuildToolGate(nil, nil, nil, after, build.CallGuard(func(context.Context, string, json.RawMessage) string {
			return "the guard says no"
		})),
	} {
		if allowed, _, _ := callBash(fn, nil); allowed || len(seen) != 0 {
			t.Fatalf("%s: the rung behind must not run for a call that is refused: allowed=%v seen=%q", name, allowed, seen)
		}
	}
	// 🚨 Its own refusal is final, behind a hook's allow too.
	last := build.AfterAllow(func(context.Context, string, json.RawMessage) string { return "no request is left" })
	fn = build.BuildToolGate(ladderEngine(t, "allow"), nil, nil, after, last)
	if allowed, reason, _ := callBash(fn, nil); allowed || reason != "no request is left" || len(seen) != 1 {
		t.Fatalf("the refusal of a rung behind must be final: allowed=%v reason=%q seen=%q", allowed, reason, seen)
	}
	// A nil rung of either kind is no rung.
	fn = build.BuildToolGate(nil, nil, nil, build.AfterAllow(nil), build.CallGuard(nil), nil)
	if allowed, reason, _ := callBash(fn, nil); !allowed {
		t.Fatalf("a nil rung must not refuse, got %q", reason)
	}
}

// 🚨 Every host's gate refuses a bash command past the bounds of the shell
// parser, ahead of the policy. The policy splits a line into its commands
// with that parser. A line that it cannot split is judged as one unit, and a
// rule that names rm does not see an rm in the middle of that unit. So
// 50,001 marks in a quoted text would hide a command from a deny rule.
func TestLadderRefusesABashCommandPastTheBoundsOfTheParser(t *testing.T) {
	withTempHome(t)
	rules := []permission.PermissionRule{{Tool: "bash", Args: regexp.MustCompile(`^rm\b`), Decision: permission.RuleDeny, Source: "user"}}
	fn := build.BuildToolGate(nil, permission.NewPolicyGate(permissions.NewPolicy(permission.ApprovalYolo, rules), nil), nil)
	check := func(command string) (bool, string) {
		args, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		allowed, reason, _ := fn.CheckTool(context.Background(), provider.ToolCallBlock{ID: "T1", Name: "bash", Arguments: args}, nil)
		return allowed, reason
	}
	if allowed, reason := check("git status"); !allowed {
		t.Fatalf("a short command must run, got %q", reason)
	}
	// Control: the policy sees the rm of a line that it can split.
	if allowed, reason := check("git status; rm -rf x"); allowed || strings.Contains(reason, "this long or this deep") {
		t.Fatalf("the deny rule must refuse the rm of a short line, got allowed=%v reason=%q", allowed, reason)
	}
	padded := "git status; rm -rf x; echo '" + strings.Repeat("(", 50001) + "'"
	if allowed, reason := check(padded); allowed || !strings.Contains(reason, "this long or this deep") {
		t.Fatalf("a line past the bounds must be refused by its length, got allowed=%v reason=%q", allowed, reason)
	}
	// A line that ended the process in the parser.
	deep := strings.Repeat("(", 250000) + "true" + strings.Repeat(")", 250000)
	if allowed, reason := check(deep); allowed || !strings.Contains(reason, "this long or this deep") {
		t.Fatalf("a deep line must be refused, got allowed=%v reason=%q", allowed, reason)
	}
	// A line of commands inside commands is within the bounds of the parser,
	// and terva does not split it either. The rm hides in it as well.
	nested := strings.Repeat("echo $(", 8000) + "rm -rf x" + strings.Repeat(")", 8000)
	if allowed, reason := check(nested); allowed || !strings.Contains(reason, "this long or this deep") {
		t.Fatalf("a line of commands inside commands must be refused, got allowed=%v reason=%q", allowed, reason)
	}
	// Another tool with a long text is no bash command.
	args, _ := json.Marshal(map[string]string{"command": deep})
	if allowed, reason, _ := fn.CheckTool(context.Background(), provider.ToolCallBlock{ID: "T2", Name: "write", Arguments: args}, nil); !allowed {
		t.Fatalf("the bounds are for bash alone, got %q", reason)
	}
}
