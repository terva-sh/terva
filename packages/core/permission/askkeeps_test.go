package permission

import (
	"encoding/json"
	"testing"
)

// keepsNotify stands for a host's AskKeeps: it keeps a notify call that goes
// to the person, and no other call.
func keepsNotify(tool string, args json.RawMessage) bool {
	var a struct {
		To string `json:"to"`
	}
	return tool == "notify" && json.Unmarshal(args, &a) == nil && a.To == "person"
}

// A call that AskKeeps names runs with no prompt in ask and auto-edit. The
// arguments decide, so the same tool still prompts when it reaches another
// party.
func TestPolicyAskKeepsRunsAPersonFacingCallUnprompted(t *testing.T) {
	toPerson := json.RawMessage(`{"to":"person"}`)
	toOther := json.RawMessage(`{"to":"other"}`)
	for _, mode := range []ApprovalMode{ApprovalAsk, ApprovalAutoEdit} {
		p := &PermissionPolicy{Mode: mode, ReadOnly: ro(), EditTools: edits(), AskKeeps: keepsNotify}
		if v, _ := p.Evaluate("notify", toPerson); v != VerdictAllow {
			t.Errorf("mode %s: a kept call got %v, want allow", mode, v)
		}
		if v, _ := p.Evaluate("notify", toOther); v != VerdictAsk {
			t.Errorf("mode %s: the same tool to another party got %v, want ask", mode, v)
		}
		if v, _ := p.Evaluate("bash", toPerson); v != VerdictAsk {
			t.Errorf("mode %s: bash got %v, want ask", mode, v)
		}
	}
}

// 🚨 AskKeeps is a default of the mode, and a rule comes first. A person who
// wrote an ask rule or a deny rule for the tool still gets what they wrote.
func TestPolicyAskKeepsGivesWayToARule(t *testing.T) {
	toPerson := json.RawMessage(`{"to":"person"}`)
	for _, mode := range []ApprovalMode{ApprovalAsk, ApprovalAutoEdit} {
		ask := &PermissionPolicy{
			Mode: mode, ReadOnly: ro(), EditTools: edits(), AskKeeps: keepsNotify,
			Rules: []PermissionRule{{Tool: "notify", Decision: RuleAsk, Source: "user"}},
		}
		if v, _ := ask.Evaluate("notify", toPerson); v != VerdictAsk {
			t.Errorf("mode %s: an ask rule got %v, want ask", mode, v)
		}
		deny := &PermissionPolicy{
			Mode: mode, ReadOnly: ro(), EditTools: edits(), AskKeeps: keepsNotify,
			Rules: []PermissionRule{{Tool: "notify", Decision: RuleDeny, Source: "user"}},
		}
		if v, _ := deny.Evaluate("notify", toPerson); v != VerdictDeny {
			t.Errorf("mode %s: a deny rule got %v, want deny", mode, v)
		}
	}
}

// AskKeeps widens ask and auto-edit alone. Plan still refuses a call that
// PlanKeeps does not name, and a policy with no AskKeeps is as it was.
func TestPolicyAskKeepsDoesNotReachPlan(t *testing.T) {
	toPerson := json.RawMessage(`{"to":"person"}`)
	plan := &PermissionPolicy{Mode: ApprovalPlan, ReadOnly: ro(), EditTools: edits(), AskKeeps: keepsNotify}
	if v, _ := plan.Evaluate("notify", toPerson); v != VerdictDeny {
		t.Errorf("plan: a call that only AskKeeps names got %v, want deny", v)
	}
	none := &PermissionPolicy{Mode: ApprovalAsk, ReadOnly: ro(), EditTools: edits()}
	if v, _ := none.Evaluate("notify", toPerson); v != VerdictAsk {
		t.Errorf("ask with no AskKeeps got %v, want ask", v)
	}
}
