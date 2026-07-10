package modes

import (
	"testing"

	"terva.sh/terva/packages/core"
)

// The shift+tab wheel cycles plan → workspace → auto-edit → plan, and any
// off-wheel mode (ask, yolo, unset) enters at the workspace default. `ask` and
// `yolo` must never be a wheel destination — a stray keypress can't drop you
// into the strictest testing posture or the most dangerous one.
func TestNextApprovalModeWheel(t *testing.T) {
	cases := []struct{ cur, want core.ApprovalMode }{
		{core.ApprovalPlan, core.ApprovalWorkspace},
		{core.ApprovalWorkspace, core.ApprovalAutoEdit},
		{core.ApprovalAutoEdit, core.ApprovalPlan}, // wrap
		{core.ApprovalAsk, core.ApprovalWorkspace}, // off-wheel → default
		{core.ApprovalYolo, core.ApprovalWorkspace},
		{core.ApprovalMode(""), core.ApprovalWorkspace},
	}
	for _, c := range cases {
		if got := nextApprovalMode(c.cur); got != c.want {
			t.Errorf("nextApprovalMode(%q) = %q, want %q", c.cur, got, c.want)
		}
	}

	// Three presses from a wheel rung return to the start (pure 3-cycle).
	m := core.ApprovalWorkspace
	for i := 0; i < 3; i++ {
		m = nextApprovalMode(m)
	}
	if m != core.ApprovalWorkspace {
		t.Errorf("three presses from workspace landed on %q, want workspace", m)
	}

	// No starting mode — on or off the wheel — ever yields ask or yolo.
	for _, start := range []core.ApprovalMode{
		core.ApprovalPlan, core.ApprovalWorkspace, core.ApprovalAutoEdit,
		core.ApprovalYolo, core.ApprovalAsk, core.ApprovalMode(""),
	} {
		if got := nextApprovalMode(start); got == core.ApprovalYolo || got == core.ApprovalAsk {
			t.Errorf("nextApprovalMode(%q) = %q, wheel must never yield ask/yolo", start, got)
		}
	}
}
