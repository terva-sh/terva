package core

import "testing"

// TestInteractiveToolAllowedEveryMode pins that an interactive tool
// (ask_user_question) is permitted in every approval mode, plan
// included, and never deferred to a prompt — gating a question behind
// approval is nonsensical.
func TestInteractiveToolAllowedEveryMode(t *testing.T) {
	modes := []ApprovalMode{ApprovalPlan, ApprovalAsk, ApprovalAutoEdit, ApprovalWorkspace, ApprovalYolo}
	for _, m := range modes {
		pol := &PermissionPolicy{
			Mode:        m,
			ReadOnly:    NewReadOnlySet(), // deliberately NOT read-only
			Interactive: map[string]bool{"ask_user_question": true},
		}
		if v, _ := pol.Evaluate("ask_user_question", nil); v != VerdictAllow {
			t.Errorf("mode %s: interactive tool verdict = %v, want VerdictAllow", m, v)
		}
		// A non-interactive, non-read-only tool must still be refused in
		// plan (proving the interactive bypass is scoped, not blanket).
		if m == ApprovalPlan {
			if v, _ := pol.Evaluate("bash", nil); v != VerdictDeny {
				t.Errorf("plan mode: non-interactive bash verdict = %v, want VerdictDeny", v)
			}
		}
	}
}
