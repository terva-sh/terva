package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/handlers"
	"terva.sh/terva/packages/core"
)

// criteriaChecked reports the checked state of every acceptance criterion, in
// the order the ticket file holds them.
func criteriaChecked(t *testing.T, tc *TicketCore, id string) []bool {
	t.Helper()
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	items := ticket.Checklist(tk.Body.AcceptanceCriteria)
	out := make([]bool, len(items))
	for i, it := range items {
		out[i] = it.Checked
	}
	return out
}

// taskForCriterion finds the seeded task that carries criterion n.
func taskForCriterion(t *testing.T, board *tasks.Store, n int) tasks.Task {
	t.Helper()
	for _, task := range board.List() {
		if task.Criterion == n {
			return task
		}
	}
	t.Fatalf("no seeded task carries criterion %d", n)
	return tasks.Task{}
}

// closeTask runs task_update through the same handler the task tool calls.
func closeTask(t *testing.T, board *tasks.Store, id, evidence string) (string, bool) {
	t.Helper()
	args := map[string]any{"id": id, "status": "done"}
	if evidence != "" {
		args["evidence"] = evidence
	}
	raw, _ := json.Marshal(args)
	return handlers.Update(board, raw)
}

// bindChecker wires the board to the ticket store the way bindTaskBoard does.
func bindChecker(board *tasks.Store, tc *TicketCore) {
	board.SetCriterionChecker(CriterionCheckerFor(core.Registry{
		"ticket_transition": &TicketTransitionTool{TicketCore: tc},
	}))
}

// The whole bridge in one test: a claim seeds tasks from the criteria, and
// closing one of those tasks with evidence ticks the criterion it came from.
// Neither store knows about the other, so this is the only place the round trip
// is visible.
func TestClosingASeededTaskChecksItsCriterion(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-criterion")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"first thing", "second thing"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	target := taskForCriterion(t, board, 2)
	text, isErr := closeTask(t, board, target.ID, "go test ./... passed")
	if isErr {
		t.Fatalf("closing the task failed: %s", text)
	}
	if !strings.Contains(text, "Checked acceptance criterion 2") {
		t.Errorf("the result does not report the criterion it checked: %s", text)
	}

	got := criteriaChecked(t, tc, id)
	want := []bool{false, true}
	if len(got) != len(want) {
		t.Fatalf("got %d criteria, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("criterion %d checked=%v, want %v", i+1, got[i], want[i])
		}
	}
}

// Evidence is the gate, not the close. An unevidenced close is exactly what the
// evidence nudge asks the model to repair, and a ticked box on a shared ticket
// is much harder to walk back than a task status.
func TestClosingWithoutEvidenceLeavesTheCriterionAlone(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-noevidence")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"only thing"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	target := taskForCriterion(t, board, 1)
	if _, isErr := closeTask(t, board, target.ID, ""); isErr {
		t.Fatal("closing without evidence should still close the task")
	}
	if got := criteriaChecked(t, tc, id); got[0] {
		t.Error("an unevidenced close ticked the acceptance criterion")
	}
}

// The criterion index counts over the FULL list, checked items included. A
// ticket with its first criterion already done seeds tasks for 2 and 3 only,
// and closing the last one must tick box 3 rather than the second box it
// occupies in the seeded list.
func TestCriterionIndexCountsCheckedItemsToo(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-index")
	bindChecker(board, tc)

	id, _ := readyTicket(t, tc, []string{"already done", "second thing", "third thing"})
	if err := board.CriterionChecker().CheckCriterion(id, 1); err != nil {
		t.Fatalf("setup: could not pre-check criterion 1: %v", err)
	}
	// The pre-check rewrote the file, so re-read the revision the claim needs.
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": cur.Revision})

	if n := len(board.List()); n != 2 {
		t.Fatalf("seeded %d tasks, want 2 (the checked criterion is skipped)", n)
	}
	target := taskForCriterion(t, board, 3)
	if _, isErr := closeTask(t, board, target.ID, "verified by hand"); isErr {
		t.Fatal("closing the task failed")
	}

	got := criteriaChecked(t, tc, id)
	want := []bool{true, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("criterion %d checked=%v, want %v (the index is off by one somewhere)", i+1, got[i], want[i])
		}
	}
}

// A task the model made itself carries no ticket linkage. Closing it must not
// reach for the ticket store at all.
func TestClosingATaskWithNoTicketLinkageChecksNothing(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-plain")
	bindChecker(board, tc)

	id, _ := readyTicket(t, tc, []string{"untouched"})
	made, err := board.Create([]tasks.CreateSpec{{Title: "unrelated work"}})
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := closeTask(t, board, made[0].ID, "done by hand")
	if isErr {
		t.Fatalf("closing a plain task failed: %s", text)
	}
	if strings.Contains(text, "acceptance criterion") {
		t.Errorf("a task with no ticket linkage reported a criterion: %s", text)
	}
	if got := criteriaChecked(t, tc, id); got[0] {
		t.Error("closing an unrelated task ticked a criterion")
	}
}
