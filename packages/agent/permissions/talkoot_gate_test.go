package permissions

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// A talkoot member in plan posture only plans, and its plan reaches the team
// as an envelope. So plan mode keeps talkoot_send and talkoot_handoff.
func TestPlanGateKeepsTheTalkootTools(t *testing.T) {
	withTempHome(t)

	plan, _ := HeadlessConfirmGate(Inputs{Mode: mode.Print, Approval: "plan", CWD: testsupport.TempDir(t)})
	if plan == nil {
		t.Fatal("plan mode must build a gate")
	}
	for _, name := range []string{"talkoot_send", "talkoot_handoff", "talkoot_roster", "talkoot_note_write", "talkoot_note_read"} {
		if ok, reason, _ := plan.Check(context.Background(), name, nil, name, ""); !ok {
			t.Errorf("plan must keep %s: %s", name, reason)
		}
		if !IsBuiltin(name) {
			t.Errorf("%s is first-party and must not prompt as foreign", name)
		}
	}
	// Plan still refuses what it refused before.
	if ok, _, _ := plan.Check(context.Background(), "write", nil, "write", ""); ok {
		t.Error("plan must still refuse write")
	}
}

// 🚨 PlanKeeps is not Interactive. An interactive tool is allowed before any
// rule runs, so a deny rule for it would stop working. A kept tool still
// faces the user's rules.
func TestADenyRuleStillStopsAKeptTool(t *testing.T) {
	p := NewPolicy(permission.ApprovalPlan, []permission.PermissionRule{{Tool: "talkoot_send", Decision: permission.RuleDeny}})
	if v, _ := p.Evaluate("talkoot_send", nil); v != permission.VerdictDeny {
		t.Errorf("a deny rule must stop talkoot_send in plan, got %v", v)
	}
	if v, _ := p.Evaluate("talkoot_handoff", nil); v != permission.VerdictAllow {
		t.Errorf("plan must allow talkoot_handoff with no rule, got %v", v)
	}
}

// Plan never prompts, so an ask rule is how a person approves each send. The
// permissions doc names it as the remedy.
func TestAnAskRuleMakesAKeptToolAsk(t *testing.T) {
	p := NewPolicy(permission.ApprovalPlan, []permission.PermissionRule{{Tool: "talkoot_send", Decision: permission.RuleAsk}})
	if v, _ := p.Evaluate("talkoot_send", nil); v != permission.VerdictAsk {
		t.Errorf("an ask rule must make talkoot_send ask in plan, got %v", v)
	}
}

// The note tools write only the talkoot's notes under $TERVA_HOME, so each
// mode treats them as it treats memory: allowed in plan, auto-edit, and
// workspace, asked in ask. A person's deny rule still stops a write.
func TestTheNoteToolsClassifyLikeMemory(t *testing.T) {
	for _, a := range []permission.ApprovalMode{permission.ApprovalPlan, permission.ApprovalAsk, permission.ApprovalAutoEdit, permission.ApprovalWorkspace} {
		p := NewPolicy(a, nil)
		want, _ := p.Evaluate("memory", nil)
		for _, name := range []string{"talkoot_note_write", "talkoot_note_read"} {
			if v, _ := p.Evaluate(name, nil); v != want {
				t.Errorf("%s in %s: %v, and memory gets %v", name, a, v, want)
			}
		}
	}
	if v, _ := NewPolicy(permission.ApprovalPlan, nil).Evaluate("talkoot_note_write", nil); v != permission.VerdictAllow {
		t.Errorf("plan must allow talkoot_note_write, got %v", v)
	}
	p := NewPolicy(permission.ApprovalPlan, []permission.PermissionRule{{Tool: "talkoot_note_write", Decision: permission.RuleDeny}})
	if v, _ := p.Evaluate("talkoot_note_write", nil); v != permission.VerdictDeny {
		t.Errorf("a deny rule must stop talkoot_note_write, got %v", v)
	}
}
