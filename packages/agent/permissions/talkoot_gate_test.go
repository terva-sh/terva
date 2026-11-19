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

// A wake starts a later turn, as a send starts one, so each mode treats
// talkoot_wake as it treats talkoot_send. A planning member that waits on a
// result may set one, and a person's rule still stops it or asks.
func TestAWakeClassifiesLikeASend(t *testing.T) {
	for _, a := range []permission.ApprovalMode{permission.ApprovalPlan, permission.ApprovalAsk, permission.ApprovalAutoEdit, permission.ApprovalWorkspace} {
		p := NewPolicy(a, nil)
		want, _ := p.Evaluate("talkoot_send", nil)
		if v, _ := p.Evaluate("talkoot_wake", nil); v != want {
			t.Errorf("talkoot_wake in %s: %v, and talkoot_send gets %v", a, v, want)
		}
	}
	if !IsBuiltin("talkoot_wake") {
		t.Error("talkoot_wake is first-party and must not prompt as foreign")
	}
	if v, _ := NewPolicy(permission.ApprovalPlan, nil).Evaluate("talkoot_wake", nil); v != permission.VerdictAllow {
		t.Errorf("plan must allow talkoot_wake with no rule, got %v", v)
	}
	deny := NewPolicy(permission.ApprovalWorkspace, []permission.PermissionRule{{Tool: "talkoot_wake", Decision: permission.RuleDeny}})
	if v, _ := deny.Evaluate("talkoot_wake", nil); v != permission.VerdictDeny {
		t.Errorf("a deny rule must stop talkoot_wake, got %v", v)
	}
	ask := NewPolicy(permission.ApprovalPlan, []permission.PermissionRule{{Tool: "talkoot_wake", Decision: permission.RuleAsk}})
	if v, _ := ask.Evaluate("talkoot_wake", nil); v != permission.VerdictAsk {
		t.Errorf("an ask rule must make talkoot_wake ask in plan, got %v", v)
	}
}

// A routine's ask writes a proposal and stores nothing, as talkoot_propose
// does. So each mode treats the two alike, and a person's rule still stops
// the ask or asks about it.
func TestARoutineAskClassifiesLikeAProposal(t *testing.T) {
	for _, a := range []permission.ApprovalMode{permission.ApprovalPlan, permission.ApprovalAsk, permission.ApprovalAutoEdit, permission.ApprovalWorkspace} {
		p := NewPolicy(a, nil)
		want, _ := p.Evaluate("talkoot_propose", nil)
		if v, _ := p.Evaluate("talkoot_routine", nil); v != want {
			t.Errorf("talkoot_routine in %s: %v, and talkoot_propose gets %v", a, v, want)
		}
	}
	if !IsBuiltin("talkoot_routine") {
		t.Error("talkoot_routine is first-party and must not prompt as foreign")
	}
	if v, _ := NewPolicy(permission.ApprovalPlan, nil).Evaluate("talkoot_routine", nil); v != permission.VerdictAllow {
		t.Errorf("plan must allow talkoot_routine with no rule, got %v", v)
	}
	deny := NewPolicy(permission.ApprovalWorkspace, []permission.PermissionRule{{Tool: "talkoot_routine", Decision: permission.RuleDeny}})
	if v, _ := deny.Evaluate("talkoot_routine", nil); v != permission.VerdictDeny {
		t.Errorf("a deny rule must stop talkoot_routine, got %v", v)
	}
}
