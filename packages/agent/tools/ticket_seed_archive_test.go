package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
)

// finishedBoard returns a board carrying one earlier ticket's work, all of it
// finished, and the id of that ticket. Done and cancelled both appear, because
// Status.IsTerminal covers the two and a test that used only done would not
// notice if that changed.
func finishedBoard(t *testing.T, tc *TicketCore, session string) (*tasks.Store, string) {
	t.Helper()
	board := seedBoard(t, session)
	old, _ := readyTicket(t, tc, []string{"The earlier ticket's criterion"})
	if _, err := board.Create([]tasks.CreateSpec{
		{Title: "Shipped the first thing", Status: tasks.StatusDone, Ticket: old, Criterion: 1},
		{Title: "Dropped the second thing", Status: tasks.StatusCancelled, Ticket: old, Criterion: 2},
	}); err != nil {
		t.Fatal(err)
	}
	return board, old
}

// The refusal is the correct half of this behavior and it stays. A mix of two
// tickets' tasks is hard to undo, so a board with real work on it stops the
// seed and says what is in the way.
//
// The archive assertion is the point of the test. Reverse the two steps and a
// claim clears the caller's board and then refuses, which leaves them nothing.
func TestClaimRefusesOpenWorkAndArchivesNothing(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "s-open")
	if _, err := board.Create([]tasks.CreateSpec{
		{Title: "Still doing this", Status: tasks.StatusPending},
		{Title: "Finished this one", Status: tasks.StatusDone},
	}); err != nil {
		t.Fatal(err)
	}
	id, rev := readyTicket(t, tc, []string{"A criterion"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || out.SeededTasks.Created != 0 {
		t.Fatalf("seeded onto a board holding open work: %+v", out.SeededTasks)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "Still doing this") {
		t.Fatalf("the refusal does not name the open task: %q", out.SeededTasks.Skipped)
	}
	if gens := board.Generations(); len(gens) != 0 {
		t.Fatalf("a refused claim archived %d generation(s); the guard must run first", len(gens))
	}
	if got := len(board.List()); got != 2 {
		t.Fatalf("board holds %d tasks, want the caller's 2 left alone", got)
	}
}

// A blocked task is work somebody parked, not work that finished. It has to
// keep blocking the seed, or a claim quietly buries a decision the user is
// waiting on.
func TestClaimCountsABlockedTaskAsOpen(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "s-blocked")
	if _, err := board.Create([]tasks.CreateSpec{
		{Title: "Parked on a decision", Status: tasks.StatusBlocked},
	}); err != nil {
		t.Fatal(err)
	}
	id, rev := readyTicket(t, tc, []string{"A criterion"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || out.SeededTasks.Created != 0 {
		t.Fatalf("a blocked task did not block the seed: %+v", out.SeededTasks)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "Parked on a decision") {
		t.Fatalf("the refusal does not name the blocked task: %q", out.SeededTasks.Skipped)
	}
	if gens := board.Generations(); len(gens) != 0 {
		t.Fatalf("a refused claim archived %d generation(s)", len(gens))
	}
}

// The behavior this ticket asked for. Without it the board carries the last
// ticket's done tasks beside the new pending ones, and every later turn
// renders both.
func TestClaimArchivesFinishedTasksBeforeItSeeds(t *testing.T) {
	tc := ticketCore(t)
	board, _ := finishedBoard(t, tc, "s-finished")
	id, rev := readyTicket(t, tc, []string{"One", "Two"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || out.SeededTasks.Created != 2 {
		t.Fatalf("seeded %+v, want 2 tasks", out.SeededTasks)
	}
	// A claim archives on the caller's behalf, so the result has to say so.
	// A silent archive is the objectionable one.
	if out.SeededTasks.Archived != 2 {
		t.Fatalf("reported %d archived, want 2", out.SeededTasks.Archived)
	}
	live := board.List()
	if len(live) != 2 {
		t.Fatalf("board holds %d tasks, want this ticket's 2 alone", len(live))
	}
	for _, task := range live {
		if task.Ticket != id {
			t.Fatalf("board still carries a task for %s: %q", task.Ticket, task.Title)
		}
	}
	if _, ok := board.Generation(1); !ok {
		t.Fatal("the finished tasks went nowhere: there is no generation 1")
	}
}

// Archiving must not cost the earlier ticket its evidence. worklogFor reads
// the generations as well as the live list, so a ticket that closes after its
// tasks were archived still writes a full worklog note.
func TestArchivedTasksStillReachTheWorklog(t *testing.T) {
	tc := ticketCore(t)
	board, old := finishedBoard(t, tc, "s-eviction")
	id, rev := readyTicket(t, tc, []string{"One"})

	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	body, skipped := worklogFor(board, old)
	if skipped != "" {
		t.Fatalf("the earlier ticket lost its worklog: %s", skipped)
	}
	for _, want := range []string{"Shipped the first thing", "Dropped the second thing"} {
		if !strings.Contains(body, want) {
			t.Fatalf("worklog lost %q after the archive:\n%s", want, body)
		}
	}
	// The new ticket must not inherit the old one's evidence.
	if fresh, _ := worklogFor(board, id); strings.Contains(fresh, "Shipped the first thing") {
		t.Fatalf("the new ticket picked up the old ticket's tasks:\n%s", fresh)
	}
}

// An empty board has nothing to roll off, and a claim must not park an empty
// generation to prove it looked.
func TestClaimOnAnEmptyBoardArchivesNothing(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "s-empty")
	id, rev := readyTicket(t, tc, []string{"One"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || out.SeededTasks.Created != 1 {
		t.Fatalf("seeded %+v, want 1 task", out.SeededTasks)
	}
	if out.SeededTasks.Archived != 0 {
		t.Fatalf("reported %d archived on an empty board", out.SeededTasks.Archived)
	}
	if gens := board.Generations(); len(gens) != 0 {
		t.Fatalf("an empty board produced %d generation(s)", len(gens))
	}
}
