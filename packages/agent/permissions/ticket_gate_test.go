package permissions

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

// The ticket write tools' headless posture, per slice 3 of
// docs/plans/git-ticket.md: a headless gate with no prompt available
// refuses what it would have asked about. The read tools stay allowed in
// plan, and the write tools are refused there and under headless ask —
// refuse-by-default, never run-unconfirmed.
func TestHeadlessGateTicketWritesRefused(t *testing.T) {
	withTempHome(t)

	plan, _ := HeadlessConfirmGate(Inputs{Mode: mode.Print, Approval: "plan", CWD: testsupport.TempDir(t)})
	if plan == nil {
		t.Fatal("plan mode must build a gate")
	}
	if ok, _, _ := plan.Check(context.Background(), "ticket_list", nil, "ticket_list", ""); !ok {
		t.Error("plan in headless should allow ticket_list (read-only)")
	}
	ok, reason, _ := plan.Check(context.Background(), "ticket_create", nil, "ticket_create", "")
	if ok {
		t.Error("plan in headless must refuse ticket_create")
	}
	if !strings.Contains(reason, "plan") {
		t.Errorf("refusal should name the mode: %q", reason)
	}

	ask, _ := HeadlessConfirmGate(Inputs{Mode: mode.Print, Approval: "ask", CWD: testsupport.TempDir(t)})
	if ask == nil {
		t.Fatal("ask mode must build a gate")
	}
	for _, name := range []string{"ticket_create", "ticket_update", "ticket_transition", "ticket_claim", "ticket_comment"} {
		if ok, _, _ := ask.Check(context.Background(), name, nil, name, ""); ok {
			t.Errorf("%s must refuse under headless ask: no prompt exists to confirm it", name)
		}
	}
}
