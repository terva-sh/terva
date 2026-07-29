package core

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func ro() *ReadOnlySet       { return NewReadOnlySet("read", "terva_status", "skill") }
func edits() map[string]bool { return map[string]bool{"write": true, "edit": true} }
func builtins() map[string]bool {
	return map[string]bool{"read": true, "write": true, "edit": true, "bash": true, "terva_status": true, "skill": true}
}

func TestParseApprovalMode(t *testing.T) {
	for _, ok := range []string{"plan", "ask", "auto-edit", "workspace", "yolo"} {
		if _, err := ParseApprovalMode(ok); err != nil {
			t.Errorf("ParseApprovalMode(%q): %v", ok, err)
		}
	}
	if _, err := ParseApprovalMode("autoedit"); err == nil {
		t.Error("want error for unknown mode")
	}
}

func TestPolicyPlanRefusesMutating(t *testing.T) {
	p := &PermissionPolicy{Mode: ApprovalPlan, ReadOnly: ro(), EditTools: edits()}
	if v, _ := p.Evaluate("read", nil); v != VerdictAllow {
		t.Errorf("plan should allow read, got %v", v)
	}
	v, reason := p.Evaluate("bash", nil)
	if v != VerdictDeny {
		t.Errorf("plan should deny bash, got %v", v)
	}
	if reason == "" {
		t.Error("plan denial needs a model-readable reason")
	}
}

func TestPolicyPlanBeatsAllowRule(t *testing.T) {
	// An allow rule must not punch a hole in an explicit plan posture.
	p := &PermissionPolicy{
		Mode:     ApprovalPlan,
		ReadOnly: ro(),
		Rules:    []PermissionRule{{Tool: "bash", Decision: RuleAllow, Source: "user"}},
	}
	if v, _ := p.Evaluate("bash", nil); v != VerdictDeny {
		t.Errorf("plan must beat allow rules, got %v", v)
	}
}

// decompAnd is a test stand-in for the agent layer's AST splitter: it
// splits a bash command on " && " into per-command args so the compound
// path can be exercised without pulling a real shell parser into core.
func decompAnd(toolName string, args json.RawMessage) []json.RawMessage {
	if toolName != "bash" {
		return nil
	}
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &a) != nil {
		return nil
	}
	parts := strings.Split(a.Command, " && ")
	if len(parts) < 2 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		b, _ := json.Marshal(map[string]string{"command": strings.TrimSpace(p)})
		out = append(out, b)
	}
	return out
}

func TestPolicyCompoundAllowRuleDoesNotClearOtherCommand(t *testing.T) {
	// `git *` is allow-listed; `git diff && rm -rf /` must still prompt
	// for the rm rather than ride the leading-command git allow.
	p := &PermissionPolicy{
		Mode:             ApprovalAsk,
		Rules:            []PermissionRule{{Tool: "bash", Args: regexp.MustCompile(`^git `), Decision: RuleAllow, Source: "user"}},
		ReadOnly:         ro(),
		Builtin:          builtins(),
		DecomposeCommand: decompAnd,
	}
	allow, _ := json.Marshal(map[string]string{"command": "git diff"})
	if v, _ := p.Evaluate("bash", allow); v != VerdictAllow {
		t.Errorf("plain `git diff` should be allowed, got %v", v)
	}
	compound, _ := json.Marshal(map[string]string{"command": "git diff && rm -rf /"})
	if v, _ := p.Evaluate("bash", compound); v != VerdictAsk {
		t.Errorf("compound with an un-allowed command should ask, got %v", v)
	}
}

func TestPolicyCompoundDenyFirst(t *testing.T) {
	// An anchored deny matches a command that isn't first on the line,
	// and a single denied command denies the whole line — even in yolo.
	p := &PermissionPolicy{
		Mode: ApprovalYolo,
		Rules: []PermissionRule{
			{Tool: "bash", Args: regexp.MustCompile(`^rm `), Decision: RuleDeny, Reason: "no deleting", Source: "project"},
		},
		ReadOnly:         ro(),
		Builtin:          builtins(),
		DecomposeCommand: decompAnd,
	}
	compound, _ := json.Marshal(map[string]string{"command": "git diff && rm -rf /"})
	v, reason := p.Evaluate("bash", compound)
	if v != VerdictDeny {
		t.Fatalf("anchored deny on a non-leading command should deny the line, got %v", v)
	}
	if !strings.Contains(reason, "no deleting") {
		t.Errorf("denial reason should carry the rule reason: %s", reason)
	}
}

func TestPolicyCompoundAllAllowedRuns(t *testing.T) {
	// When every command on the line is allow-listed, the line auto-runs.
	p := &PermissionPolicy{
		Mode:             ApprovalAsk,
		Rules:            []PermissionRule{{Tool: "bash", Args: regexp.MustCompile(`^(git|ls) `), Decision: RuleAllow, Source: "user"}},
		ReadOnly:         ro(),
		Builtin:          builtins(),
		DecomposeCommand: decompAnd,
	}
	compound, _ := json.Marshal(map[string]string{"command": "git diff && ls -la"})
	if v, _ := p.Evaluate("bash", compound); v != VerdictAllow {
		t.Errorf("every command allow-listed should auto-run, got %v", v)
	}
}

// The split engages with ZERO rules too. Today that is behaviorally invisible
// (every scope resolves to the same mode default), and this test pins the
// mechanism so it stays true: the day any default becomes arg-sensitive, a
// rules-gated split would silently judge a rules-free session's
// `git diff && rm -rf /` as one unit. The sandbox splits unconditionally;
// the policy must keep matching it.
func TestPolicyCompoundSplitsWithoutRules(t *testing.T) {
	calls := 0
	p := &PermissionPolicy{
		Mode:     ApprovalAsk,
		ReadOnly: ro(),
		Builtin:  builtins(),
		DecomposeCommand: func(toolName string, args json.RawMessage) []json.RawMessage {
			calls++
			return decompAnd(toolName, args)
		},
	}
	compound, _ := json.Marshal(map[string]string{"command": "git diff && rm -rf /"})
	if v, _ := p.Evaluate("bash", compound); v != VerdictAsk {
		t.Errorf("rules-free ask mode should ask, got %v", v)
	}
	if calls != 1 {
		t.Errorf("decomposer should run once with zero rules, ran %d times", calls)
	}
}

func TestPolicyRuleFirstMatchWins(t *testing.T) {
	p := &PermissionPolicy{
		Mode: ApprovalYolo,
		Rules: []PermissionRule{
			{Tool: "bash", Args: regexp.MustCompile(`^rm `), Decision: RuleDeny, Reason: "no deleting", Source: "project"},
			{Tool: "bash", Decision: RuleAllow, Source: "user"},
		},
		ReadOnly: ro(), EditTools: edits(),
	}
	args, _ := json.Marshal(map[string]string{"command": "rm -rf build"})
	v, reason := p.Evaluate("bash", args)
	if v != VerdictDeny {
		t.Fatalf("deny rule should win, got %v", v)
	}
	for _, want := range []string{"project", "no deleting"} {
		if !strings.Contains(reason, want) {
			t.Errorf("denial reason should contain %q: %s", want, reason)
		}
	}
	args2, _ := json.Marshal(map[string]string{"command": "ls"})
	if v, _ := p.Evaluate("bash", args2); v != VerdictAllow {
		t.Errorf("fallthrough allow rule should match, got %v", v)
	}
}

func TestPolicyRuleGlobAndArgsMatching(t *testing.T) {
	// Uses auto-edit (not yolo) so an ask match is observable — yolo
	// degrades ask to allow.
	p := &PermissionPolicy{
		Mode:     ApprovalAutoEdit,
		Rules:    []PermissionRule{{Tool: "mcp_*", Decision: RuleAsk, Source: "user"}},
		ReadOnly: ro(), EditTools: edits(),
	}
	if v, _ := p.Evaluate("mcp_github_create_issue", nil); v != VerdictAsk {
		t.Error("glob rule should match mcp_ prefix")
	}
	if v, _ := p.Evaluate("read", nil); v != VerdictAllow {
		t.Error("glob rule must not match unrelated tools (read is auto-allowed)")
	}
}

func TestPolicyArgsMatchesTopLevelStringValues(t *testing.T) {
	// The pattern is anchored to the command value, not the JSON
	// encoding around it.
	r := PermissionRule{Tool: "bash", Args: regexp.MustCompile(`^git (status|diff)\b`), Decision: RuleAllow, Source: "user"}
	args, _ := json.Marshal(map[string]string{"command": "git status"})
	if !r.matches("bash", args) {
		t.Error("want match on top-level string value")
	}
	argsNo, _ := json.Marshal(map[string]string{"command": "echo git status"})
	if r.matches("bash", argsNo) {
		t.Error("anchored pattern must not match mid-string")
	}
}

func TestPolicyWorkspaceMode(t *testing.T) {
	// Workspace: built-ins and reads (including foreign read-only)
	// run; foreign side-effecting tools ask. The read-only set holds
	// the built-in reads plus a foreign read-only tool added at merge.
	roSet := NewReadOnlySet("read", "terva_status", "skill", "web_search")
	p := &PermissionPolicy{Mode: ApprovalWorkspace, ReadOnly: roSet, EditTools: edits(), Builtin: builtins()}
	cases := map[string]PolicyVerdict{
		"read":          VerdictAllow, // built-in read-only
		"write":         VerdictAllow, // built-in mutating — trusted, sandbox bounds it
		"bash":          VerdictAllow, // built-in
		"web_search":    VerdictAllow, // foreign but read-only
		"web_fetch_raw": VerdictAsk,   // foreign + side effects
		"mcp_github_x":  VerdictAsk,   // foreign + side effects
	}
	for tool, want := range cases {
		if v, _ := p.Evaluate(tool, nil); v != want {
			t.Errorf("workspace tool %s: got %v, want %v", tool, v, want)
		}
	}
}

func TestPolicyModeDefaults(t *testing.T) {
	cases := []struct {
		mode ApprovalMode
		tool string
		want PolicyVerdict
	}{
		{ApprovalYolo, "bash", VerdictAllow},
		{ApprovalAsk, "read", VerdictAsk}, // ask prompts for everything — the --no-yolo contract
		{ApprovalAsk, "bash", VerdictAsk},
		{ApprovalAutoEdit, "read", VerdictAllow},
		{ApprovalAutoEdit, "write", VerdictAllow},
		{ApprovalAutoEdit, "edit", VerdictAllow},
		{ApprovalAutoEdit, "bash", VerdictAsk},
		{ApprovalAutoEdit, "ext_tool", VerdictAsk}, // unknown tools are mutating
	}
	for _, c := range cases {
		p := &PermissionPolicy{Mode: c.mode, ReadOnly: ro(), EditTools: edits()}
		if v, _ := p.Evaluate(c.tool, nil); v != c.want {
			t.Errorf("mode %s tool %s: got %v, want %v", c.mode, c.tool, v, c.want)
		}
	}
}

func TestPolicyAskRuleForcesPromptOutsideYolo(t *testing.T) {
	// ask still forces a prompt in a mode that would otherwise
	// auto-allow (auto-edit auto-allows reads).
	p := &PermissionPolicy{
		Mode:     ApprovalAutoEdit,
		Rules:    []PermissionRule{{Tool: "read", Decision: RuleAsk, Source: "user"}},
		ReadOnly: ro(), EditTools: edits(),
	}
	if v, _ := p.Evaluate("read", nil); v != VerdictAsk {
		t.Error("ask rule should force a prompt in auto-edit")
	}
}

func TestPolicyYoloNeverPromptsButDenyStillBlocks(t *testing.T) {
	// yolo means "never prompt me": an ask rule degrades to allow.
	// A deny rule, however, still blocks even in yolo — deny, not ask,
	// is how you stop something there.
	p := &PermissionPolicy{
		Mode: ApprovalYolo,
		Rules: []PermissionRule{
			{Tool: "web_fetch_raw", Decision: RuleAsk, Source: "extension web"},
			{Tool: "bash", Args: regexp.MustCompile(`rm -rf`), Decision: RuleDeny, Reason: "no", Source: "user"},
		},
		ReadOnly: ro(), EditTools: edits(),
	}
	if v, _ := p.Evaluate("web_fetch_raw", nil); v != VerdictAllow {
		t.Errorf("yolo should not prompt for an ask-ruled tool, got %v", v)
	}
	args, _ := json.Marshal(map[string]string{"command": "rm -rf /"})
	if v, _ := p.Evaluate("bash", args); v != VerdictDeny {
		t.Errorf("a deny rule must still block in yolo, got %v", v)
	}
}

func TestGateDenyRuleBeatsSessionAlwaysAllow(t *testing.T) {
	// "Always allow bash" remembered in-session must not override an
	// explicit config deny rule: config outranks convenience.
	pol := &PermissionPolicy{
		Mode:     ApprovalAsk,
		Rules:    []PermissionRule{{Tool: "bash", Args: regexp.MustCompile(`rm -rf`), Decision: RuleDeny, Source: "user"}},
		ReadOnly: ro(), EditTools: edits(),
	}
	g := NewPolicyGate(pol, confirmFunc(func(tool, preview string) ConfirmDecision {
		return ConfirmDecision{Allow: true, RememberTool: true}
	}))
	lsArgs, _ := json.Marshal(map[string]string{"command": "ls"})
	if ok, _, _ := g.Check(context.Background(), "bash", lsArgs, "ls", ""); !ok {
		t.Fatal("first call should be allowed via confirmer")
	}
	rmArgs, _ := json.Marshal(map[string]string{"command": "rm -rf /tmp/x"})
	if ok, _, _ := g.Check(context.Background(), "bash", rmArgs, "rm -rf /tmp/x", ""); ok {
		t.Fatal("deny rule must beat remembered always-allow")
	}
}

func TestGatePersistCallback(t *testing.T) {
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalAsk, ReadOnly: ro(), EditTools: edits()},
		confirmFunc(func(tool, preview string) ConfirmDecision {
			return ConfirmDecision{Allow: true, PersistTool: true}
		}))
	var persisted [][2]string
	g.SetPersist(func(tool, pattern string) { persisted = append(persisted, [2]string{tool, pattern}) })

	if ok, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !ok {
		t.Fatal("persist answer should allow the call")
	}
	if len(persisted) != 1 || persisted[0] != [2]string{"bash", ""} {
		t.Fatalf("persist callback got %v, want one blanket bash grant", persisted)
	}
	// PersistTool implies session memory too: no second prompt.
	calls := len(persisted)
	if ok, _, _ := g.Check(context.Background(), "bash", nil, "pwd", ""); !ok {
		t.Fatal("second call should ride session memory")
	}
	if len(persisted) != calls {
		t.Error("session-cached call must not re-fire persist")
	}
}

// A scoped grant (PersistScopes) persists one rule per pattern and — unlike
// the blanket PersistTool — must NOT blanket-allow the tool for the session:
// the saved rules take over for matching calls after the host's policy
// refresh, and everything else keeps prompting.
func TestGateScopedPersistSkipsSessionBlanket(t *testing.T) {
	prompts := 0
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalAsk, ReadOnly: ro(), EditTools: edits()},
		confirmFunc(func(tool, preview string) ConfirmDecision {
			prompts++
			return ConfirmDecision{Allow: true, PersistTool: true,
				PersistScopes: []string{`^git status(?:\s|$)`}}
		}))
	var persisted [][2]string
	g.SetPersist(func(tool, pattern string) { persisted = append(persisted, [2]string{tool, pattern}) })

	if ok, _, _ := g.Check(context.Background(), "bash", nil, "git status", ""); !ok {
		t.Fatal("scoped persist answer should allow the call")
	}
	want := [2]string{"bash", `^git status(?:\s|$)`}
	if len(persisted) != 1 || persisted[0] != want {
		t.Fatalf("persist callback got %v, want [%v]", persisted, want)
	}
	// The next bash call must prompt again — no session-wide grant rode along.
	if ok, _, _ := g.Check(context.Background(), "bash", nil, "rm -rf /tmp/x", ""); !ok {
		t.Fatal("second call should still be allowed (confirmer allows)")
	}
	if prompts != 2 {
		t.Fatalf("prompts = %d, want 2 — a scoped grant must not blanket the session", prompts)
	}
}

// The gate hands a ConfirmerWithRequest the derived scopes from the
// injected deriver, and prefers that interface over ConfirmerWithCall.
func TestGateScopeDeriverReachesConfirmer(t *testing.T) {
	var got ConfirmRequest
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalAsk, ReadOnly: ro(), EditTools: edits()},
		confirmReqFunc(func(req ConfirmRequest) ConfirmDecision {
			got = req
			return ConfirmDecision{Allow: false, Reason: "just looking"}
		}))
	g.SetScopeDeriver(func(tool string, args json.RawMessage) []GrantScope {
		return []GrantScope{{Display: "git status", Pattern: `^git status(?:\s|$)`}}
	})
	if ok, _, _ := g.Check(context.Background(), "bash", nil, "git status", "call_7"); ok {
		t.Fatal("confirmer denied; gate must deny")
	}
	if got.Tool != "bash" || got.CallID != "call_7" || len(got.Scopes) != 1 || got.Scopes[0].Display != "git status" {
		t.Fatalf("ConfirmRequest = %+v, want tool/call id/derived scope threaded through", got)
	}
}

func TestGateSetModeSwitchesEnforcement(t *testing.T) {
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalYolo, ReadOnly: ro(), EditTools: edits()}, nil)
	if g.Mode() != ApprovalYolo {
		t.Fatalf("initial mode = %s", g.Mode())
	}
	if ok, _, _ := g.Check(context.Background(), "bash", nil, "", ""); !ok {
		t.Fatal("yolo should allow bash")
	}
	g.SetMode(ApprovalPlan)
	if g.Mode() != ApprovalPlan {
		t.Fatalf("mode after SetMode = %s", g.Mode())
	}
	if ok, _, _ := g.Check(context.Background(), "bash", nil, "", ""); ok {
		t.Error("plan should refuse bash after live switch")
	}
	if ok, _, _ := g.Check(context.Background(), "read", nil, "", ""); !ok {
		t.Error("plan should still allow read")
	}
}

func TestGateRulesAndGrantsSnapshot(t *testing.T) {
	pol := &PermissionPolicy{
		Mode:     ApprovalAsk,
		Rules:    []PermissionRule{{Tool: "bash", Decision: RuleDeny, Source: "user"}},
		ReadOnly: ro(), EditTools: edits(),
	}
	g := NewPolicyGate(pol, confirmFunc(func(string, string) ConfirmDecision {
		return ConfirmDecision{Allow: true, RememberTool: true}
	}))
	if rules := g.Rules(); len(rules) != 1 || rules[0].Tool != "bash" {
		t.Fatalf("Rules() = %+v", rules)
	}
	if ok, _, _ := g.Check(context.Background(), "read", nil, "", ""); !ok {
		t.Fatal("ask should allow once via confirmer")
	}
	all, tools := g.Grants()
	if all || len(tools) != 1 || tools[0] != "read" {
		t.Errorf("Grants() = %v %v, want false [read]", all, tools)
	}
}

func TestGateSetModeRaceSafe(t *testing.T) {
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalYolo, ReadOnly: ro(), EditTools: edits()}, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			if i%2 == 0 {
				g.SetMode(ApprovalPlan)
			} else {
				g.SetMode(ApprovalYolo)
			}
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_, _, _ = g.Check(context.Background(), "bash", nil, "", "")
		_ = g.Mode()
	}
	<-done
}

// confirmFunc adapts a func to the Confirmer interface for tests.
type confirmFunc func(toolName, preview string) ConfirmDecision

func (f confirmFunc) Confirm(_ context.Context, toolName, preview string) ConfirmDecision {
	return f(toolName, preview)
}

// confirmReqFunc adapts a func to ConfirmerWithRequest for tests.
type confirmReqFunc func(req ConfirmRequest) ConfirmDecision

func (f confirmReqFunc) Confirm(_ context.Context, toolName, preview string) ConfirmDecision {
	return f(ConfirmRequest{Tool: toolName, Preview: preview})
}
func (f confirmReqFunc) ConfirmWithRequest(_ context.Context, req ConfirmRequest) ConfirmDecision {
	return f(req)
}

// TestIsReadOnlyAuthority: only the two low-risk local classes (local-read
// and the new local-data) are auto-allowable; everything else, and
// empty/unknown, is gated.
func TestIsReadOnlyAuthority(t *testing.T) {
	for _, a := range []Authority{AuthLocalRead, AuthLocalData} {
		if !IsReadOnlyAuthority(string(a)) {
			t.Errorf("%q should be auto-allowable (read-only-equivalent)", a)
		}
	}
	for _, a := range []Authority{
		AuthWorkspaceMutate, AuthProcessExec, AuthNetworkRead,
		AuthExternalMutate, AuthUserInteraction, "", "totally-unknown",
	} {
		if IsReadOnlyAuthority(string(a)) {
			t.Errorf("%q must NOT be auto-allowable", a)
		}
	}
}
