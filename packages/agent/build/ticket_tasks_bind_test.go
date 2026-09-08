package build

import (
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// claimBoard returns the task board that reg's ticket_claim is bound to.
func claimBoard(t *testing.T, reg core.Registry) tools.TaskBoard {
	t.Helper()
	tl, err := reg.Get("ticket_claim")
	if err != nil {
		t.Fatalf("ticket_claim missing from the registry: %v", err)
	}
	ct, ok := tl.(*tools.TicketClaimTool)
	if !ok {
		t.Fatalf("ticket_claim is %T, not *tools.TicketClaimTool", tl)
	}
	return ct.Tasks
}

// transitionBoard returns the task board that reg's ticket_transition is bound
// to. A closing transition writes the worklog from it.
func transitionBoard(t *testing.T, reg core.Registry) tools.TaskBoard {
	t.Helper()
	tl, err := reg.Get("ticket_transition")
	if err != nil {
		t.Fatalf("ticket_transition missing from the registry: %v", err)
	}
	tt, ok := tl.(*tools.TicketTransitionTool)
	if !ok {
		t.Fatalf("ticket_transition is %T, not *tools.TicketTransitionTool", tl)
	}
	return tt.Tasks
}

// sharedTicketResolved is sharedTasksResolved plus the two ticket tools that
// touch the task board, bound to the shared board exactly as Resolve binds it.
func sharedTicketResolved(t *testing.T) (Resolved, *tasktool.Controller) {
	t.Helper()
	r, shared := sharedTasksResolved()
	tc := &tools.TicketCore{CWD: testsupport.TempDir(t)}
	r.ToolRegistry["ticket_claim"] = &tools.TicketClaimTool{TicketCore: tc}
	r.ToolRegistry["ticket_transition"] = &tools.TicketTransitionTool{TicketCore: tc}
	bindTaskBoard(r.ToolRegistry, shared)
	if claimBoard(t, r.ToolRegistry) != tools.TaskBoard(shared.Store()) {
		t.Fatal("setup: ticket_claim did not bind to the shared board")
	}
	if transitionBoard(t, r.ToolRegistry) != tools.TaskBoard(shared.Store()) {
		t.Fatal("setup: ticket_transition did not bind to the shared board")
	}
	return r, shared
}

// ticket_transition writes the worklog from the board, so it needs the same
// per-group rebind ticket_claim needs. Without it, closing a ticket in an
// admitted group would copy the owner DM's private task history into a shared
// ticket file.
func TestFreshTasksRegistryRebindsTicketTransition(t *testing.T) {
	r, shared := sharedTicketResolved(t)

	regA, ctrlA := r.freshTasksRegistry()
	regB, ctrlB := r.freshTasksRegistry()

	boardA, boardB := transitionBoard(t, regA), transitionBoard(t, regB)
	if boardA == tools.TaskBoard(shared.Store()) {
		t.Error("group A ticket_transition still reads the owner board")
	}
	if boardB == tools.TaskBoard(shared.Store()) {
		t.Error("group B ticket_transition still reads the owner board")
	}
	if boardA != tools.TaskBoard(ctrlA.Store()) {
		t.Error("group A ticket_transition is not bound to group A's board")
	}
	if boardB != tools.TaskBoard(ctrlB.Store()) {
		t.Error("group B ticket_transition is not bound to group B's board")
	}
}

// The bridge runs both ways, and both directions bind in one place so they
// cannot drift. A board with no checker closes tasks that never tick their
// criterion, which fails silently: the task list looks right and the ticket
// never moves.
func TestBindTaskBoardBindsTheCriterionChecker(t *testing.T) {
	r, shared := sharedTicketResolved(t)
	if shared.Store().CriterionChecker() == nil {
		t.Fatal("the shared board has no criterion checker, so closing a task ticks nothing")
	}

	// Every per-group board needs its own, for the same reason.
	regA, ctrlA := r.freshTasksRegistry()
	_ = regA
	if ctrlA.Store().CriterionChecker() == nil {
		t.Error("a group board has no criterion checker")
	}
}

// A --tools allowlist or plan mode can drop the ticket tools. The board must
// then forget the checker rather than keep writing to a store the session no
// longer exposes.
func TestBindTaskBoardClearsTheCheckerWithoutTicketTools(t *testing.T) {
	r, shared := sharedTicketResolved(t)
	if shared.Store().CriterionChecker() == nil {
		t.Fatal("setup: wanted a checker to clear")
	}

	delete(r.ToolRegistry, "ticket_claim")
	delete(r.ToolRegistry, "ticket_transition")
	bindTaskBoard(r.ToolRegistry, shared)

	if shared.Store().CriterionChecker() != nil {
		t.Error("the board kept a criterion checker after the ticket tools were dropped")
	}
}

// The bot-mode isolation seam has to cover ticket_claim, not only the task
// tools. freshTasksRegistry copies the registry shallowly, so without a rebind
// the group's ticket_claim stays the shared instance and a claim made in an
// admitted group seeds the owner DM's durable board.
func TestFreshTasksRegistryRebindsTicketClaim(t *testing.T) {
	r, shared := sharedTicketResolved(t)

	regA, ctrlA := r.freshTasksRegistry()
	regB, ctrlB := r.freshTasksRegistry()

	boardA, boardB := claimBoard(t, regA), claimBoard(t, regB)
	if boardA == tools.TaskBoard(shared.Store()) {
		t.Error("group A ticket_claim still seeds the owner board")
	}
	if boardB == tools.TaskBoard(shared.Store()) {
		t.Error("group B ticket_claim still seeds the owner board")
	}
	if boardA == boardB {
		t.Error("both groups' ticket_claim share one board")
	}
	if boardA != tools.TaskBoard(ctrlA.Store()) {
		t.Error("group A ticket_claim is not bound to group A's board")
	}
	if boardB != tools.TaskBoard(ctrlB.Store()) {
		t.Error("group B ticket_claim is not bound to group B's board")
	}
	// The owner's own registry keeps the owner board.
	if claimBoard(t, r.ToolRegistry) != tools.TaskBoard(shared.Store()) {
		t.Error("cloning a group registry moved the owner's own binding")
	}
}

// UseTasks carries the board across a tool rebuild, which fires on any
// extension policy assertion and on entering plan mode. ticket_claim has to
// travel with it, or a claim would report tasks that task_list never shows.
func TestUseTasksRebindsTicketClaim(t *testing.T) {
	r, shared := sharedTicketResolved(t)

	_, replacement := sharedTasksResolved()
	if replacement == shared {
		t.Fatal("setup: wanted a distinct controller")
	}
	r.UseTasks(replacement)

	if got := claimBoard(t, r.ToolRegistry); got != tools.TaskBoard(replacement.Store()) {
		t.Error("ticket_claim kept the board from before the rebuild")
	}
}

// A --tools allowlist or plan mode can drop ticket_claim. Rebinding must not
// resurrect it.
func TestBindTaskBoardDoesNotResurrectADroppedClaim(t *testing.T) {
	r, _ := sharedTicketResolved(t)
	delete(r.ToolRegistry, "ticket_claim")

	_, replacement := sharedTasksResolved()
	r.UseTasks(replacement)

	if _, err := r.ToolRegistry.Get("ticket_claim"); err == nil {
		t.Error("rebinding put a dropped ticket_claim back in the registry")
	}
}
