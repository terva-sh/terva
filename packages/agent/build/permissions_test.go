package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func TestResolveApprovalModePrecedence(t *testing.T) {
	cases := []struct {
		name string
		args Args
		cfg  config.Config
		want core.ApprovalMode
	}{
		{"interactive default is workspace", Args{Mode: ModeInteractive}, config.Config{}, core.ApprovalWorkspace},
		{"headless default is yolo", Args{Mode: ModePrint}, config.Config{}, core.ApprovalYolo},
		{"acp default is workspace (interactive editor)", Args{Mode: ModeACP}, config.Config{}, core.ApprovalWorkspace},
		{"no-yolo aliases ask", Args{Mode: ModeInteractive, NoYolo: true}, config.Config{}, core.ApprovalAsk},
		{"flag beats no-yolo", Args{NoYolo: true, Approval: "plan"}, config.Config{}, core.ApprovalPlan},
		{"config default applies (interactive)", Args{Mode: ModeInteractive}, config.Config{Approval: "auto-edit"}, core.ApprovalAutoEdit},
		{"flag beats config", Args{Approval: "yolo"}, config.Config{Approval: "ask"}, core.ApprovalYolo},
		{"no-yolo beats config", Args{NoYolo: true}, config.Config{Approval: "yolo"}, core.ApprovalAsk},
		{"bad config value falls back to interactive default", Args{Mode: ModeInteractive}, config.Config{Approval: "bogus"}, core.ApprovalWorkspace},
	}
	for _, c := range cases {
		if got := ResolveApprovalMode(c.args, c.cfg); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestResolveJail(t *testing.T) {
	cases := []struct {
		name string
		args Args
		want bool
	}{
		{"interactive jails by default", Args{Mode: ModeInteractive}, true},
		{"acp jails by default", Args{Mode: ModeACP}, true},
		{"headless does not jail by default", Args{Mode: ModePrint}, false},
		{"--no-jail forces off in interactive", Args{Mode: ModeInteractive, NoJail: true}, false},
		{"--jail forces on in headless", Args{Mode: ModePrint, Jail: true}, true},
		{"--no-jail beats --jail", Args{Mode: ModeInteractive, Jail: true, NoJail: true}, false},
	}
	for _, c := range cases {
		if got := resolveJail(c.args); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCompileRulesProjectCannotAllow(t *testing.T) {
	rules := []config.PermissionRuleConfig{
		{Tool: "bash", Decision: "allow"},
		{Tool: "bash", Args: "rm", Decision: "deny", Reason: "nope"},
		{Tool: "read", Decision: "ask"},
	}
	out, warns := compilePermissionRules(rules, "project", true)
	if len(out) != 2 {
		t.Fatalf("want allow rule dropped, got %d rules", len(out))
	}
	for _, r := range out {
		if r.Decision == core.RuleAllow {
			t.Fatal("project layer produced an allow rule")
		}
		if r.Source != "project" {
			t.Errorf("rule source = %q, want project", r.Source)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "may not allow") {
		t.Errorf("want a self-approval-ban warning, got %v", warns)
	}
	// The same rule set in the trusted user layer keeps its allow.
	out, warns = compilePermissionRules(rules, "user", false)
	if len(out) != 3 || len(warns) != 0 {
		t.Errorf("user layer: got %d rules %d warns, want 3/0", len(out), len(warns))
	}
}

func TestCompileRulesDropsBrokenNotStartup(t *testing.T) {
	rules := []config.PermissionRuleConfig{
		{Tool: "", Decision: "deny"},               // no tool
		{Tool: "bash", Decision: "maybe"},          // bad decision
		{Tool: "bash", Args: "(", Decision: "ask"}, // bad regexp
		{Tool: "bash", Decision: "deny"},           // fine
	}
	out, warns := compilePermissionRules(rules, "user", false)
	if len(out) != 1 {
		t.Fatalf("want 1 surviving rule, got %d", len(out))
	}
	if len(warns) != 3 {
		t.Errorf("want 3 warnings, got %d: %v", len(warns), warns)
	}
}

// withTempHome points TERVA_HOME at a temp dir for config isolation.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}

func TestBuildPermissionPolicyNilOnPureYolo(t *testing.T) {
	withTempHome(t)
	pol, warns := BuildPermissionPolicy(Args{CWD: testsupport.TempDir(t)})
	if pol != nil {
		t.Fatalf("pure yolo should produce a nil policy (no-gate fast path), got %+v", pol)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestBuildPermissionPolicyUserIsSovereign(t *testing.T) {
	home := withTempHome(t)
	cfgJSON := `{"permissions":[{"tool":"bash","decision":"allow"}]}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(proj, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"permissions":[{"tool":"bash","args":"rm","decision":"deny","reason":"project says no"}]}`
	if err := os.WriteFile(filepath.Join(proj, ".terva", "config.json"), []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	pol, _ := BuildPermissionPolicy(Args{CWD: proj})
	if pol == nil {
		t.Fatal("rules exist; policy must be non-nil even in yolo")
	}
	if len(pol.Rules) != 2 || pol.Rules[0].Source != "user" || pol.Rules[1].Source != "project" {
		t.Fatalf("want [user project] rule order, got %+v", pol.Rules)
	}
	// The user is sovereign: an explicit user allow beats a project
	// deny, even one targeting the same call.
	args, _ := json.Marshal(map[string]string{"command": "rm -rf /"})
	if v, _ := pol.Evaluate("bash", args); v != core.VerdictAllow {
		t.Errorf("user allow should beat project deny, got %v", v)
	}
}

func TestBuildPermissionPolicyProjectBeatsExtensionWhereUserSilent(t *testing.T) {
	home := withTempHome(t)
	// No user rule for the tool — so the restrict-only layers decide.
	writeExtension(t, home, "e", map[string]any{
		"permissions": []map[string]any{{"tool": "web_fetch_raw", "decision": "ask"}},
	}, nil)
	proj := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(proj, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"permissions":[{"tool":"web_fetch_raw","decision":"deny","reason":"not in this repo"}]}`
	if err := os.WriteFile(filepath.Join(proj, ".terva", "config.json"), []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	pol, _ := BuildPermissionPolicy(Args{CWD: proj})
	if v, reason := pol.Evaluate("web_fetch_raw", nil); v != core.VerdictDeny || !strings.Contains(reason, "not in this repo") {
		t.Errorf("project deny should beat extension ask where the user is silent: %v %q", v, reason)
	}
}

func TestHeadlessGatePlanModeAllowsReadOnly(t *testing.T) {
	withTempHome(t)
	gate, _ := HeadlessConfirmGate(Args{Approval: "plan", CWD: testsupport.TempDir(t)}, "print")
	if gate == nil {
		t.Fatal("plan mode must build a gate")
	}
	if ok, _, _ := gate.Check("read", nil, "f.txt"); !ok {
		t.Error("plan in headless should allow read")
	}
	ok, reason, _ := gate.Check("bash", nil, "ls")
	if ok {
		t.Error("plan in headless must refuse bash")
	}
	if !strings.Contains(reason, "plan") {
		t.Errorf("refusal should name the mode: %q", reason)
	}
}

func TestHeadlessGateAllowRuleRunsWithoutPrompt(t *testing.T) {
	home := withTempHome(t)
	cfgJSON := `{"approval":"ask","permissions":[{"tool":"bash","args":"^git status$","decision":"allow"}]}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	gate, _ := HeadlessConfirmGate(Args{CWD: testsupport.TempDir(t)}, "json")
	if gate == nil {
		t.Fatal("ask mode must build a gate")
	}
	okArgs, _ := json.Marshal(map[string]string{"command": "git status"})
	if ok, _, _ := gate.Check("bash", okArgs, "git status"); !ok {
		t.Error("explicit allow rule should run headless without a prompt")
	}
	otherArgs, _ := json.Marshal(map[string]string{"command": "git push"})
	if ok, _, _ := gate.Check("bash", otherArgs, "git push"); ok {
		t.Error("unmatched call in ask mode must refuse headless")
	}
}

func TestAppendUserPermissionRulePersists(t *testing.T) {
	withTempHome(t)
	if err := AppendUserPermissionRule("bash"); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second grant doesn't duplicate.
	if err := AppendUserPermissionRule("bash"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Permissions) != 1 || cfg.Permissions[0].Tool != "bash" || cfg.Permissions[0].Decision != "allow" {
		t.Fatalf("persisted rules = %+v", cfg.Permissions)
	}
}

func TestPlanModeFiltersToolRegistry(t *testing.T) {
	reg := BuildToolRegistry(Args{}, core.ApprovalPlan, testsupport.TempDir(t), nil, "anthropic", "apikey", true, nil)
	for name := range reg {
		// Plan keeps read-only tools plus interactive tools
		// (ask_user_question) — asking the user is exactly what plan
		// mode wants when requirements are unclear.
		if !readOnlyTools[name] && !interactiveTools[name] {
			t.Errorf("plan registry leaked mutating tool %s", name)
		}
	}
	if _, ok := reg["read"]; !ok {
		t.Error("plan registry should keep read")
	}
	if _, ok := reg["ask_user_question"]; !ok {
		t.Error("plan registry should keep the interactive ask_user_question tool")
	}
	if _, ok := reg["bash"]; ok {
		t.Error("plan registry must not contain bash")
	}
}
