//go:build terva_acp

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// ACP is NOT a fixed-trust host. /trust and /untrust flip Workspace Trust over
// the wire, mid-session, and until this the flip moved exactly one thing: the
// extension manager. The hook engine and the model's tool set kept the answer
// they were built with at launch, so a newly trusted repo's pre-tool-use hooks
// and project skills waited for the editor to open a NEW session — while the
// confirmation said the project content was live.
//
// These are the behavioural half of the claim. The static census in
// workspace/trust_census_test.go can say a reader was classified; it cannot tell
// a reader that captured the launch answer from one that re-resolves, because
// both look identical to a parser.

// acpProjectDenyHook writes a project config whose pre-tool-use hook denies
// every call by exiting 2 — the shell-ergonomic deny spelling the ladder
// honours. (`false` exits 1, which reads as "no opinion".)
func acpProjectDenyHook(t *testing.T, cwd string) {
	t.Helper()
	dir := filepath.Join(cwd, ".terva")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	cfg := map[string]any{
		"hooks": map[string]any{
			"pre_tool_use": []map[string]any{{
				"command": "sh",
				"args":    []string{"-c", "echo 'denied by the project hook' >&2; exit 2"},
			}},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// acpProjectSkill writes a project SKILL.md — content trust gates, and which the
// model reaches through the `skill` tool rather than the system prompt, so a
// rebuilt tool set is observable without re-rendering the prompt.
func acpProjectSkill(t *testing.T, cwd, name string) {
	t.Helper()
	dir := filepath.Join(cwd, ".terva", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: a project skill\n---\n\ndo the project thing\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// allowingConfirmer stands in for the editor answering
// session/request_permission with "yes". ACP always builds a gate, and without a
// confirmer every gated call is refused — which would mask the hook verdict
// these tests are actually asking about.
type allowingConfirmer struct{}

func (allowingConfirmer) Confirm(string, string) core.ConfirmDecision {
	return core.ConfirmDecision{Allow: true}
}

// acpTestSession builds a real ACP session agent for cwd the way
// NewSessionAgent does, and hands back the two trust closures the acp package
// receives — NOT the bare applier. That distinction is load-bearing: the
// closures persist the verdict before applying it, and the rebuild re-resolves
// against the store, exactly as Workspace.Trust does. A test that applied
// without persisting would be exercising an order production never uses.
func acpTestSession(t *testing.T, cwd string, mcpServers json.RawMessage) (ag *core.Agent, trust func(bool) error, untrust func() error, r build.Resolved) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	// WithSkills is the CLI parser's default, not Args' zero value — without it
	// Discover never scans a search dir at all and the project-skill assertion
	// below would pass for the wrong reason.
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, WithSkills: true}
	if len(mcpServers) == 0 {
		args.NoMCP = true
	}
	ctx := context.Background()
	f := &acpFactory{ctx: ctx, args: args, version: "test"}
	r, ag, _, cleanup, _, _, applyTrust, err := f.buildAgent(ctx, cwd, mcpServers, allowingConfirmer{})
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
	t.Cleanup(cleanup)
	if applyTrust == nil {
		t.Fatal("buildAgent wired no applyTrust — /trust would persist the verdict and change nothing about the open session")
	}
	trust, untrust = acpTrustWorkspace(ctx, cwd, applyTrust), acpUntrustWorkspace(ctx, cwd, applyTrust)
	if trust == nil || untrust == nil {
		t.Fatal("the ACP trust closures came back nil, so /trust and /untrust would degrade to notes")
	}
	return ag, trust, untrust, r
}

// acpHookVerdict runs the agent's own tool-call ladder — the path every real
// tool call takes — and reports whether the call was denied and why.
func acpHookVerdict(t *testing.T, ag *core.Agent) (denied bool, reason string) {
	t.Helper()
	if ag.BeforeToolExecute == nil {
		t.Fatal("the ACP agent wired no tool-call ladder, so nothing could gate a call")
	}
	allowed, why, _ := ag.BeforeToolExecute(provider.ToolCallBlock{
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"echo hi"}`),
	})
	return !allowed, why
}

// Project hooks are a trust-gated EXECUTION path, so the direction that matters
// is both ways: trusting must start them, and untrusting must stop them.
func TestAnACPTrustFlipStartsAndStopsProjectHooks(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell to run a hook program")
	}
	cwd := testsupport.TempDir(t)
	acpProjectDenyHook(t, cwd)
	ag, trust, untrust, _ := acpTestSession(t, cwd, nil)

	// Untrusted: the hook is on disk and must NOT run. This is the security
	// direction — a repo you have not trusted does not get to execute a program
	// on every tool call.
	if denied, reason := acpHookVerdict(t, ag); denied {
		t.Fatalf("an UNTRUSTED project's hook ran and denied the call (%q) — trust is not gating execution under ACP", reason)
	}

	if err := trust(false); err != nil {
		t.Fatalf("/trust: %v", err)
	}
	denied, reason := acpHookVerdict(t, ag)
	if !denied {
		t.Error("after /trust the project's pre-tool-use hook still does not run — ACP was holding its launch verdict, " +
			"so a newly trusted repo's hooks waited for the editor to open a new session")
	}
	if denied && reason == "" {
		t.Error("the deny carried no reason; the hook's stderr is what tells the model why it was refused")
	}

	if err := untrust(); err != nil {
		t.Fatalf("/untrust: %v", err)
	}
	if denied, _ := acpHookVerdict(t, ag); denied {
		t.Error("after /untrust the project's hook is still running — revoking trust must stop executing the repo's " +
			"programs, or /untrust is advisory exactly where it matters most")
	}
}

// The third surface, and the one that survived the first pass: keyed LORE. It
// is trust-gated like skills, but it reaches the model through the agent's
// per-turn context provider rather than the tool registry — so rebuilding the
// tool set does not touch it, and a newly trusted project's lore went on being
// invisible for the life of the session. Constant lore is a different matter:
// it is prompt-baked and still lands on a new session.
func TestAnACPTrustFlipLetsAProjectsKeyedLoreFire(t *testing.T) {
	cwd := testsupport.TempDir(t)
	dir := filepath.Join(cwd, ".terva", "lore")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir lore dir: %v", err)
	}
	entry := "---\nname: dragons\nkeys: [dragon]\n---\nDragons hoard ACP-LORE-MARKER.\n"
	if err := os.WriteFile(filepath.Join(dir, "dragons.md"), []byte(entry), 0o644); err != nil {
		t.Fatalf("write lore entry: %v", err)
	}
	ag, trust, untrust, _ := acpTestSession(t, cwd, nil)
	// The key has to appear in the transcript or the entry never activates,
	// trusted or not — without this the assertions below would both pass and
	// mean nothing.
	ag.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "tell me about the dragon"}},
	}})

	if strings.Contains(ag.ContextPreview(), "ACP-LORE-MARKER") {
		t.Fatal("an UNTRUSTED project's lore already reached the model — trust is not gating lore discovery")
	}
	if err := trust(false); err != nil {
		t.Fatalf("/trust: %v", err)
	}
	if !strings.Contains(ag.ContextPreview(), "ACP-LORE-MARKER") {
		t.Error("after /trust the project's keyed lore still does not fire — the agent kept the context provider it " +
			"was built with, so this half of the flip waited for a new session while the confirmation said otherwise")
	}
	if err := untrust(); err != nil {
		t.Fatalf("/untrust: %v", err)
	}
	if strings.Contains(ag.ContextPreview(), "ACP-LORE-MARKER") {
		t.Error("after /untrust the project's lore is still being injected — withdrawal has to reach every surface " +
			"the grant did")
	}
}

// The other half: a trust flip has to reach the model's TOOL SET, not just the
// extension subprocesses. Project skills are the cheapest proof — Resolve gates
// the project skill dirs on the verdict it re-reads, and the `skill` tool is how
// the model loads one.
func TestAnACPTrustFlipPutsAProjectSkillInReachOfTheModel(t *testing.T) {
	cwd := testsupport.TempDir(t)
	acpProjectSkill(t, cwd, "project-only-skill")
	ag, trust, untrust, _ := acpTestSession(t, cwd, nil)

	if acpSkillNames(t, ag)["project-only-skill"] {
		t.Fatal("an UNTRUSTED project's skill was already loadable — trust is not gating project skill dirs")
	}
	if err := trust(false); err != nil {
		t.Fatalf("/trust: %v", err)
	}
	if !acpSkillNames(t, ag)["project-only-skill"] {
		t.Error("after /trust the project's skill is still not loadable — the tool set the model sees was never " +
			"rebuilt, so the extensions and skills a trust flip admits were running where nothing could call them")
	}
	if err := untrust(); err != nil {
		t.Fatalf("/untrust: %v", err)
	}
	if acpSkillNames(t, ag)["project-only-skill"] {
		t.Error("after /untrust the project's skill is still loadable — the withdrawal has to reach the tool set too")
	}
}

func acpSkillNames(t *testing.T, ag *core.Agent) map[string]bool {
	t.Helper()
	reg := ag.ToolsSnapshot()
	st, ok := reg["skill"].(*skills.Tool)
	if !ok {
		t.Fatalf("no skill tool in the agent's registry (%d tools) — this test cannot say anything without one", len(reg))
	}
	out := map[string]bool{}
	for _, s := range st.Skills() {
		out[s.Name] = true
	}
	return out
}

// A rebuild re-resolves from scratch, and a fresh Resolve mints a fresh sandbox.
// ACP has /jail and /unjail, which mutate the sandbox through the pointer the
// acp session holds — so if the rebuilt tools took the new one, those two verbs
// would go on adjusting an object no tool consults.
func TestAnACPToolRebuildKeepsTheSandboxTheJailVerbsMutate(t *testing.T) {
	cwd := testsupport.TempDir(t)
	ag, trust, _, r := acpTestSession(t, cwd, nil)
	if r.Sandbox == nil {
		t.Skip("this build resolved no sandbox, so there is nothing for /jail to move")
	}

	if err := trust(false); err != nil {
		t.Fatalf("/trust: %v", err)
	}

	var checked int
	for name, tool := range ag.ToolsSnapshot() {
		var got *tools.Sandbox
		switch v := tool.(type) {
		case *tools.ReadTool:
			got = v.Sandbox
		case *tools.BashTool:
			got = v.Sandbox
		case *tools.WriteTool:
			got = v.Sandbox
		default:
			continue
		}
		checked++
		if got != r.Sandbox {
			t.Errorf("after a trust flip the %q tool holds a DIFFERENT sandbox than the one /jail and /unjail move — "+
				"the jail verbs would report success and change nothing", name)
		}
	}
	if checked == 0 {
		t.Fatal("no sandboxed tool survived the rebuild, so this test proved nothing about the sandbox")
	}
}

// The editor owns the MCP server set under ACP, and a fresh Resolve carries none
// of it. Re-merging the adapter is what keeps those tools in front of the model
// across a trust flip or a /reload-ext.
func TestAnACPToolRebuildKeepsTheEditorsMCPTools(t *testing.T) {
	stub := buildACPMCPStub(t)
	cwd := testsupport.TempDir(t)
	servers, err := json.Marshal([]map[string]any{{"name": "stub", "command": stub}})
	if err != nil {
		t.Fatalf("marshal mcpServers: %v", err)
	}
	ag, trust, _, _ := acpTestSession(t, cwd, servers)

	before := acpMCPToolNames(ag)
	if len(before) == 0 {
		t.Fatal("the editor's MCP server contributed no tools at build time, so this test cannot detect losing them")
	}
	if err := trust(false); err != nil {
		t.Fatalf("/trust: %v", err)
	}
	after := acpMCPToolNames(ag)
	for _, name := range before {
		if !contains(after, name) {
			t.Errorf("the editor's MCP tool %q disappeared from the model's tool set after a trust flip — a fresh "+
				"Resolve carries no MCP tools, so the rebuild has to re-merge the adapter", name)
		}
	}
}

func acpMCPToolNames(ag *core.Agent) []string {
	var out []string
	for name := range ag.ToolsSnapshot() {
		if strings.Contains(name, "stub") {
			out = append(out, name)
		}
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func buildACPMCPStub(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "mcpstub")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "terva.sh/terva/packages/agent/mcp/testdata/cmd/mcpstub")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the mcp stub in this environment: %v: %s", err, b)
	}
	return out
}

// The two closures do two different jobs, and doing only the first is what left
// ACP holding a launch snapshot: persisting is what /trust MEANS, applying is
// what makes it true for the session already open.
func TestACPTrustClosuresPersistAndApply(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)

	var got []bool
	apply := func(_ context.Context, trusted bool) { got = append(got, trusted) }

	trust := acpTrustWorkspace(context.Background(), cwd, apply)
	if trust == nil {
		t.Fatal("acpTrustWorkspace returned nil with a live applier — /trust would degrade to a note")
	}
	if err := trust(false); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if !permissionsTrusted(t, cwd) {
		t.Error("/trust did not persist the verdict, so the next launch would be restricted again")
	}
	if len(got) != 1 || !got[0] {
		t.Errorf("/trust applied %v; want exactly one apply(true) — persisting without applying is the gap this closed", got)
	}

	untrust := acpUntrustWorkspace(context.Background(), cwd, apply)
	if untrust == nil {
		t.Fatal("acpUntrustWorkspace returned nil with a live applier")
	}
	if err := untrust(); err != nil {
		t.Fatalf("untrust: %v", err)
	}
	if permissionsTrusted(t, cwd) {
		t.Error("/untrust did not drop the verdict from the store")
	}
	if len(got) != 2 || got[1] {
		t.Errorf("/untrust applied %v; want a second apply(false)", got)
	}

	// A host that wires no applier keeps the documented degradation: the ACP
	// command reports trust is unavailable rather than silently persisting a
	// verdict nothing will act on.
	if acpTrustWorkspace(context.Background(), cwd, nil) != nil {
		t.Error("acpTrustWorkspace with no applier must return nil so /trust degrades to a note")
	}
	if acpUntrustWorkspace(context.Background(), cwd, nil) != nil {
		t.Error("acpUntrustWorkspace with no applier must return nil so /untrust degrades to a note")
	}
}

func permissionsTrusted(t *testing.T, cwd string) bool {
	t.Helper()
	store, err := config.LoadTrustStore()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	ok, _ := store.IsTrusted(cwd)
	return ok
}

var _ = fmt.Sprintf
