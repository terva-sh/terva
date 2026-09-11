package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// seedBoard is a live-only task board bound to a session id, which is what a
// claim reads when it records where the work happened.
func seedBoard(t *testing.T, sessionID string) *tasks.Store {
	t.Helper()
	b := tasks.NewStore(tasks.NewDirFS(testsupport.TempDir(t)), "agent")
	if sessionID != "" {
		if err := b.Rebind(sessionID); err != nil {
			t.Fatalf("Rebind(%q): %v", sessionID, err)
		}
	}
	return b
}

// readyTicket creates a ticket with the given acceptance criteria and promotes
// it to ready, because a draft cannot be claimed. It returns the id and the
// revision a claim must pass as if_revision.
func readyTicket(t *testing.T, tc *TicketCore, criteria []string) (id, revision string) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"title":               "Bridge ticket",
		"acceptance_criteria": criteria,
	})
	res, err := (&TicketCreateTool{TicketCore: tc}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("create refused: %s", ticketResultText(t, res))
	}
	var made ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &made); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]any{"ref": made.ID, "if_revision": made.Revision, "status": "ready"})
	res, err = (&TicketTransitionTool{TicketCore: tc}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("promote refused: %s", ticketResultText(t, res))
	}
	var ready ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &ready); err != nil {
		t.Fatal(err)
	}
	return ready.ID, ready.Revision
}

// runClaim executes ticket_claim bound to board and returns the parsed payload.
func runClaim(t *testing.T, tc *TicketCore, board TaskBoard, args map[string]any) ticketClaimOut {
	t.Helper()
	tool := (&TicketClaimTool{TicketCore: tc}).WithTasks(board)
	raw, _ := json.Marshal(args)
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("claim refused: %s", ticketResultText(t, res))
	}
	var out ticketClaimOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func ticketCore(t *testing.T) *TicketCore {
	t.Helper()
	tc := ticketToolStore(t, 0)
	tc.ActorID = "agent:terva/testbot"
	tc.ActorName = "Testbot"
	return tc
}

// A claim seeds one task per acceptance criterion, and each task carries the
// ticket and the criterion index it came from. That linkage is the whole
// bridge: nothing in the ticket store records which task covers which
// criterion.
func TestClaimSeedsTasksFromAcceptanceCriteria(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, rev := readyTicket(t, tc, []string{"Parse the input", "Render the output", "Document the flag"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil {
		t.Fatal("claim reported no seeding at all")
	}
	if out.SeededTasks.Created != 3 {
		t.Fatalf("seeded %d tasks, want 3 (skipped: %q)", out.SeededTasks.Created, out.SeededTasks.Skipped)
	}
	// The success shape stays as it was: a seed that worked carries no reason,
	// so a caller can read Skipped as "something stopped this" and nothing else.
	if out.SeededTasks.Skipped != "" {
		t.Errorf("a successful seed carried a skip reason: %q", out.SeededTasks.Skipped)
	}
	list := board.List()
	if len(list) != 3 {
		t.Fatalf("board holds %d tasks, want 3", len(list))
	}
	wantTitles := []string{"Parse the input", "Render the output", "Document the flag"}
	for i, task := range list {
		if task.Title != wantTitles[i] {
			t.Errorf("task %d title = %q, want %q", i, task.Title, wantTitles[i])
		}
		if task.Ticket != id {
			t.Errorf("task %d ticket = %q, want %q", i, task.Ticket, id)
		}
		if task.Criterion != i+1 {
			t.Errorf("task %d criterion = %d, want %d", i, task.Criterion, i+1)
		}
	}
}

// The index a seeded task carries must address the FULL criteria list, checked
// items included, because that is what ticket.SetChecklistItem addresses. A
// task numbered off the filtered slice would check the wrong box, silently.
func TestClaimSkipsCheckedCriteriaButKeepsTheirIndices(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, rev := readyTicket(t, tc, []string{"Already done", "Still open", "Also open"})

	// Check the first criterion the way a finished task would.
	s, err := ticket.Discover(tc.CWD)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Apply(context.Background(), id,
		ticket.SetChecklistItem{Section: ticket.AcceptanceCriteria, Index: 1, Checked: true},
		ticket.ApplyOptions{IfRevision: rev, Actor: tc.actor()})
	if err != nil {
		t.Fatal(err)
	}

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": res.Ticket.Revision})

	if out.SeededTasks == nil || out.SeededTasks.Created != 2 {
		t.Fatalf("seeded %+v, want 2 tasks", out.SeededTasks)
	}
	list := board.List()
	if len(list) != 2 {
		t.Fatalf("board holds %d tasks, want 2", len(list))
	}
	// Indices 2 and 3, not 1 and 2.
	if list[0].Title != "Still open" || list[0].Criterion != 2 {
		t.Errorf("first seeded task = %q/%d, want \"Still open\"/2", list[0].Title, list[0].Criterion)
	}
	if list[1].Title != "Also open" || list[1].Criterion != 3 {
		t.Errorf("second seeded task = %q/%d, want \"Also open\"/3", list[1].Title, list[1].Criterion)
	}
}

// seed_tasks:false claims the ticket and leaves the board alone, and it now
// says what that cost. This test asserted the silence until 2026-09-09. The
// session that changed it passed the argument four times and then checked the
// criteria of all four tickets with `edit`, because nothing told it that the
// argument had removed its only tool route to a criterion.
func TestClaimSeedTasksFalseSaysWhatItGaveUp(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, rev := readyTicket(t, tc, []string{"Parse the input", "Render the output"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev, "seed_tasks": false})

	if out.SeededTasks == nil {
		t.Fatal("the opt-out reported nothing, so a caller cannot tell it gave up the criteria route")
	}
	if out.SeededTasks.Created != 0 || len(out.SeededTasks.TaskIDs) != 0 {
		t.Errorf("the opt-out seeded after all: %+v", out.SeededTasks)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "seed_tasks") {
		t.Errorf("the reason should name the argument that caused it, got %q", out.SeededTasks.Skipped)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "close") {
		t.Errorf("the reason should name what checks a criterion, got %q", out.SeededTasks.Skipped)
	}
	if n := len(board.List()); n != 0 {
		t.Errorf("opt-out seeded %d tasks", n)
	}
	if out.ClaimedBy != tc.ActorID {
		t.Errorf("the claim itself did not land: claimed_by = %q", out.ClaimedBy)
	}
}

// A boardless session stays silent even on an opt-out. There is no task tool
// to reach for, so a reason that names one would be advice the model cannot
// take. This is the one case where silence is still right.
func TestClaimOptOutWithoutABoardStaysSilent(t *testing.T) {
	tc := ticketCore(t)
	id, rev := readyTicket(t, tc, []string{"Parse the input"})

	out := runClaim(t, tc, nil, map[string]any{"ref": id, "if_revision": rev, "seed_tasks": false})

	if out.SeededTasks != nil {
		t.Errorf("a boardless opt-out reported seeding: %+v", out.SeededTasks)
	}
	if out.ClaimedBy != tc.ActorID {
		t.Fatalf("the claim did not land: claimed_by = %q", out.ClaimedBy)
	}
}

// The third no-seed reason: the ticket carries criteria and every one is
// already checked. Seeding a checked criterion as a pending task would invite
// the model to redo work that is finished.
func TestClaimWithEveryCriterionCheckedSaysSo(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, _ := readyTicket(t, tc, []string{"Parse the input", "Render the output"})
	checkEveryCriterion(t, tc, id, 2)

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": currentRevision(t, tc, id)})

	if out.SeededTasks == nil {
		t.Fatal("a finished ticket reported no seeding at all")
	}
	if !strings.Contains(out.SeededTasks.Skipped, "already checked") {
		t.Errorf("want an every-criterion-checked reason, got %q", out.SeededTasks.Skipped)
	}
	if n := len(board.List()); n != 0 {
		t.Errorf("seeded %d tasks for a ticket with nothing left", n)
	}
}

// checkEveryCriterion ticks the first n acceptance criteria through the store,
// which is the same positional index a seeded task carries.
func checkEveryCriterion(t *testing.T, tc *TicketCore, id string, n int) {
	t.Helper()
	s, err := ticket.Discover(tc.CWD)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		cur, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(context.Background(), id, ticket.SetChecklistItem{
			Section: ticket.AcceptanceCriteria,
			Index:   i,
			Checked: true,
		}, ticket.ApplyOptions{IfRevision: cur.Revision, Actor: ticket.Actor{ID: tc.ActorID, Name: tc.ActorName}}); err != nil {
			t.Fatalf("check criterion %d: %v", i, err)
		}
	}
}

// A board with open work is not seeded onto, because mixing two tickets' tasks
// cannot be undone. The refusal names the reason and the claim still lands.
func TestClaimRefusesToSeedOntoAnOpenBoard(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	if _, err := board.Create([]tasks.CreateSpec{{Title: "leftover work"}}); err != nil {
		t.Fatal(err)
	}
	id, rev := readyTicket(t, tc, []string{"Parse the input"})

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || out.SeededTasks.Created != 0 {
		t.Fatalf("seeded onto an open board: %+v", out.SeededTasks)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "task_archive") {
		t.Errorf("refusal should name the remedy, got %q", out.SeededTasks.Skipped)
	}
	if !strings.Contains(out.SeededTasks.Skipped, "leftover work") {
		t.Errorf("refusal should name what is in the way, got %q", out.SeededTasks.Skipped)
	}
	if n := len(board.List()); n != 1 {
		t.Errorf("board grew to %d tasks", n)
	}
	if out.ClaimedBy != tc.ActorID {
		t.Errorf("a skipped seeding must not fail the claim: claimed_by = %q", out.ClaimedBy)
	}
}

// A ticket with no acceptance criteria seeds nothing and says why, rather than
// reporting a silent success.
func TestClaimWithoutCriteriaSaysSo(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, rev := readyTicket(t, tc, nil)

	out := runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks == nil || !strings.Contains(out.SeededTasks.Skipped, "no acceptance criteria") {
		t.Fatalf("want a no-criteria reason, got %+v", out.SeededTasks)
	}
}

// AC 4: the claim records the terva session id, so a ticket indexes into
// transcript history.
func TestClaimRecordsTheSessionID(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-abc")
	id, rev := readyTicket(t, tc, []string{"Parse the input"})

	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	s, err := ticket.Discover(tc.CWD)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Claim == nil {
		t.Fatal("no claim on the ticket")
	}
	if tk.Claim.Session == nil || *tk.Claim.Session != "sess-abc" {
		t.Errorf("claim session = %v, want sess-abc", tk.Claim.Session)
	}
}

// A session with no task board claims normally and records no session id,
// rather than inventing one that points at no transcript.
func TestClaimWithoutABoardStillWorks(t *testing.T) {
	tc := ticketCore(t)
	id, rev := readyTicket(t, tc, []string{"Parse the input"})

	out := runClaim(t, tc, nil, map[string]any{"ref": id, "if_revision": rev})

	if out.SeededTasks != nil {
		t.Errorf("a boardless session reported seeding: %+v", out.SeededTasks)
	}
	if out.ClaimedBy != tc.ActorID {
		t.Fatalf("the claim did not land: claimed_by = %q", out.ClaimedBy)
	}
}

// WithTasks must return a new tool. A rebinder that mutated in place would
// rebind every copy that shares the instance, which is how a group chat would
// reach the owner's board.
func TestWithTasksReturnsANewInstance(t *testing.T) {
	tc := ticketCore(t)
	base := &TicketClaimTool{TicketCore: tc}
	a := base.WithTasks(seedBoard(t, "a"))
	b := base.WithTasks(seedBoard(t, "b"))

	if a == b {
		t.Fatal("WithTasks returned the same instance twice")
	}
	if core.Tool(base) == a {
		t.Fatal("WithTasks mutated the receiver instead of copying")
	}
	ca, cb := a.(*TicketClaimTool), b.(*TicketClaimTool)
	if ca.Tasks == cb.Tasks {
		t.Fatal("both copies share one board")
	}
	if base.Tasks != nil {
		t.Fatal("the receiver gained a board")
	}
}
